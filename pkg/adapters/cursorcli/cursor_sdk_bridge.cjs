// Thin transport for the official Cursor SDK (local runtime). Policy and accounting stay in Go.
// One bridge per chat: it keeps the SDK agent between turns. Lines in: start, prompt, steer, cancel.
// Lines out: ready, delta, tool, usage, steer_accepted, result, fatal. The SDK logs to stdout
// too, so every protocol line carries a marker and Go ignores the rest.
const { createInterface } = require('node:readline');
const emit = value => process.stdout.write('\u0001aw ' + JSON.stringify(value) + '\n');
const short = error => String(error?.message || error || 'Error').split('\n')[0].slice(0, 300);

async function main() {
  // The SDK finds its bundled programs (ripgrep for grep/glob, the tree-sitter shell parser) in
  // node_modules above the running script. This script sits in a temp folder, so point argv[1] at
  // the SDK itself; otherwise every grep/glob fails "Ripgrep path not configured".
  process.argv[1] = process.argv[2];
  const { Agent, Cursor } = require(process.argv[2]);
  let agent, run, start, store, followups = [], early = [], busy = false;
  // This process belongs to one account; keep a successful discovery across starts/resumes. The lookup begins as
  // the bridge starts, so a start rarely waits, and never keeps a failed or empty result: the next start asks again
  // (PLAT-872-64: an empty result cached for the bridge's life sent the bare model without the account's fixed
  // settings, and a Plus account refused it). A start waits up to 15 s, asking again after a quick failure: the
  // earlier 3 s limit started about one Cursor chat in six without the settings, and Plus refused it (PLAT-872-93).
  // Go remembers the settings a start found, so later chats of this account skip the lookup.
  let accountModels, catalog = 'not_needed', lookup, lookupMs;
  const listModels = () => lookup ??= Cursor.models.list({ apiKey: process.env.CURSOR_API_KEY || undefined })
    .then(models => { if (Array.isArray(models) && models.length) accountModels = models; else catalog = 'empty'; return models; },
          error => { catalog = 'error: ' + short(error); })
    .finally(() => { lookup = undefined; });
  listModels();
  const modelDefaults = async (limit = 15000) => { // limit: tests shorten the wait
    const began = Date.now();
    while (!accountModels) {
      const left = limit - (Date.now() - began);
      if (left <= 0) { catalog = 'timeout'; break; }
      let timer;
      const waited = await Promise.race([listModels().then(() => 'done'), new Promise(resolve => { timer = setTimeout(() => resolve('timeout'), left); })]);
      clearTimeout(timer);
      if (waited === 'timeout') { catalog = 'timeout'; break; }
      if (!accountModels) await new Promise(resolve => setTimeout(resolve, Math.min(1000, Math.max(0, limit - (Date.now() - began)))));
    }
    lookupMs = Date.now() - began;
    if (!accountModels) return []; // Discovery trouble must not prevent an otherwise valid chat.
    catalog = 'ok';
    return accountModels;
  };
  // Cancels this agent's runs still marked running; true when there was one. Throws only when a stale
  // run could not be cancelled; if the runs cannot be listed, the agent is kept as before.
  const cancelStale = async agentId => {
    const where = { runtime: 'local', cwd: start.cwd };
    let cursor, found = false;
    do {
      let page;
      try { page = await Agent.listRuns(agentId, { ...where, cursor }); } catch { return found; }
      for (const old of page.items) {
        if (old.status === 'running') { await Agent.cancelRun(old.id, where); found = true; }
      }
      cursor = page.nextCursor;
    } while (cursor);
    return found;
  };
  // Never leave a run active at Cursor when this bridge is stopped: a server restart (SIGTERM) or Go
  // closing stdin cancels the in-flight run first, so the next resume of this agent can send again.
  const cancelRun = () => {
    let timer;
    return Promise.race([run?.cancel().catch(() => {}), new Promise(resolve => { timer = setTimeout(resolve, 3000); })]).finally(() => clearTimeout(timer));
  };
  for (const signal of ['SIGTERM', 'SIGHUP', 'SIGINT']) {
    process.on(signal, () => { drop(); cancelRun().finally(() => process.exit(143)); });
  }
  const lines = createInterface({ input: process.stdin });
  const queue = [];
  let wake;

  const next = async () => {
    while (!queue.length) await new Promise(resolve => { wake = resolve; });
    wake = undefined;
    return queue.shift();
  };
  const options = () => ({
    apiKey: process.env.CURSOR_API_KEY || undefined,
    model: start.model,
    // Rules (system prompt) and skills come from this chat's own folder Go prepared (start.dirs); its MCP
    // servers arrive explicitly in start.mcp_servers.
    local: { cwd: start.cwd, ...(store ? { store } : {}), ...(start.dirs ? { dirs: start.dirs } : {}), settingSources: ['project', 'user'],
      // Task subagents get this agent's tool allowlist; without it a child gets Cursor's full toolset.
      subagentInherit: {} },
    ...(start.mode ? { mode: start.mode } : {}),
    ...(start.tools ? { tools: start.tools } : {}),
    ...(start.mcp_servers ? { mcpServers: start.mcp_servers } : {}),
  });
  // Steers handed to a running run. The SDK settles a steer only when the server answers, and one it
  // had queued is settled "revert_to_followup" only as its run ends, often after run.wait() returned.
  // So a turn waits for every steer's outcome before it ends, and answers each one the run did not
  // take (or never confirmed) as a follow-up: an accepted message is never dropped.
  let steers = [];
  const settle = (entry, outcome) => {
    if (entry.settled) return;
    entry.settled = true;
    emit({ type: 'steer_outcome', id: entry.id, status: outcome });
    if (outcome !== 'complete_delivered') followups.push(entry.text);
  };
  const drop = () => { for (const entry of steers) entry.settled = true; steers = []; followups = []; };
  // Steering and cancel arrive while a run streams, so they are handled as they are read.
  const control = request => {
    if (request.type === 'steer') {
      // Go only steers during its turn; before this turn's prompt arrives, hold it for that prompt.
      if (!busy) { early.push(request.text); emit({ type: 'steer_accepted', id: request.id, accepted: true }); return true; }
      // The prompt arrived but its run is still starting: answer it right after that run, same turn.
      if (!run) { followups.push(request.text); emit({ type: 'steer_accepted', id: request.id, accepted: true }); return true; }
      emit({ type: 'steer_accepted', id: request.id, accepted: true });
      const entry = { id: request.id, text: request.text };
      steers.push(entry);
      entry.done = (run.steer ? run.steer(request.text) : Promise.resolve('revert_to_followup'))
        .then(outcome => settle(entry, outcome), () => settle(entry, 'error'));
      return true;
    }
    if (request.type === 'cancel') { drop(); run?.cancel().catch(() => {}); return true; }
    return false;
  };
  lines.on('line', line => {
    const request = JSON.parse(line);
    if ((busy || request.type === 'steer') && control(request)) return;
    queue.push(request); wake?.();
  });
  lines.on('close', () => { if (busy) { drop(); cancelRun(); } queue.push({ type: 'close' }); wake?.(); });
  for (;;) {
    const request = await next();
    if (request.type === 'close') break;
    if (control(request)) continue;
    if (request.type === 'start') {
      start = request;
      if (start.store_root) {
        // One private store per AgentWorks chat. The shared SDK index may
        // contain other conversations and must never enter a transfer.
        const { SqliteLocalAgentStore } = require(require('node:path').join(require('node:path').dirname(process.argv[2]), 'sqlite.js'));
        store = await SqliteLocalAgentStore.open({ workspaceRef: start.cwd, stateRoot: start.store_root });
      }
      if (!process.env.CURSOR_API_KEY && (await Cursor.auth.status()).status !== 'logged-in') {
        throw new Error('Sign in again to use Cursor. Open Providers, select this account, and choose Sign in.');
      }
      // PLAT-872-64: an omitted parameter list is not the account's default variant.
      // Composer's account catalog, for example, supplies explicit fast=false.
      // Preserve explicit selections; resolve bare IDs with this account only.
      if (!start.model.params?.length) {
        const models = await modelDefaults(start.catalog_wait_ms);
        const variant = models.find(model => model.id === start.model.id)?.variants?.find(variant => variant.isDefault);
        if (variant?.params?.length) start.model = {...start.model, params: variant.params};
      }
      if (start.agent_id) {
        try { agent = await Agent.resume(start.agent_id, options()); }
        catch (error) {
          // A saved agent this store does not have: start fresh and let Go send the history.
          if (start.require_native_resume || !/not.?found|unknown agent|does not exist/i.test(short(error))) throw error;
          agent = undefined;
        }
      }
      // A run this chat's earlier bridge left active (killed by a restart before run.cancel landed) blocks
      // every later send with "already has active run". This bridge has started no run yet, so any
      // running run is stale: cancel it, or start a new agent and let Go send the history.
      let stale;
      if (agent) {
        try { stale = (await cancelStale(agent.agentId)) ? 'cancelled' : undefined; }
        catch { stale = 'new_agent'; agent = undefined; }
      }
      const resumed = Boolean(agent);
      if (start.require_native_resume && !resumed) throw new Error('The transferred native Cursor agent could not be resumed. Its saved history was kept.');
      if (start.require_native_resume && agent.agentId !== start.agent_id) throw new Error('Cursor returned a different native agent. The transferred chat was kept.');
      if (!agent) agent = await Agent.create(options());
      emit({ type: 'ready', agent_id: agent.agentId, resumed, ...(stale ? { stale, stale_agent: start.agent_id } : {}), model_catalog: catalog, ...(lookupMs !== undefined ? { model_lookup_ms: lookupMs } : {}), model_params: (start.model.params || []).map(p => p.id + '=' + p.value).join(',') });
      continue;
    }
    if (request.type !== 'prompt') continue;
    busy = true;
    followups = early;
    early = [];
    let text = request.text, last;
    try {
      while (text !== undefined) {
        // After Cursor says its turn ended, a brief stream error can make the SDK resume the same run
        // from an older checkpoint; those resumes save nothing and the run only ends much later, if at
        // all (RTS 12 Nov: the answer was out, then 15 silent minutes). So once the turn has ended and
        // nothing new starts for a short while, the answer already streamed is the reply: the run is
        // cancelled and the turn finishes. A run that ends with the SDK's own "no progress" error after
        // the turn ended has finished too.
        let turnEnded = false, quietEnded = false, quiet, giveUp;
        const outstandingWork = new Set(), detachedWork = new Set();
        const hasWork = () => outstandingWork.size > 0 || detachedWork.size > 0;
        const stopQuiet = () => { clearTimeout(quiet); quiet = undefined; };
        const quietMs = start.turn_end_quiet_ms ?? 30000;
        const gaveUp = new Promise(resolve => { giveUp = resolve; });
        const armQuiet = () => {
          stopQuiet();
          if (!turnEnded || hasWork()) return;
          quiet = setTimeout(() => {
            if (!turnEnded || hasWork()) return;
            quietEnded = true;
            emit({ type: 'turn_end_settled', action: 'quiet', status: String(quietMs) });
            run?.cancel().catch(() => {});
            // A cancel the SDK never answers must not hold a truly idle turn.
            setTimeout(() => giveUp({ status: 'finished', result: '' }), 10000);
          }, quietMs);
        };
        const sendOptions = {
          // Reassert the selected variant on every turn, including resumed/follow-up runs.
          model: start.model,
          onDelta: ({ update }) => {
            // The SDK exposes call lifetimes, not a separate background roster.
            // A detached task remains known work until the native run finishes.
            if (update.type === 'tool-call-started' && ['shell', 'task'].includes(update.toolCall?.type)) {
              outstandingWork.add(update.callId);
              stopQuiet();
            } else if (update.type === 'tool-call-delta') {
              outstandingWork.add(update.callId);
              stopQuiet();
            } else if (update.type === 'tool-call-completed' && ['shell', 'task'].includes(update.toolCall?.type)) {
              outstandingWork.delete(update.callId);
              if (update.toolCall.type === 'task' && update.toolCall.result?.status === 'success' && update.toolCall.result.value?.isBackground === true) detachedWork.add(update.callId);
              else detachedWork.delete(update.callId);
              armQuiet();
            }
            if (update.type === 'turn-ended') {
              turnEnded = true;
              emit({ type: 'turn_ended' });
              armQuiet();
            } else if (['step-started', 'user-message-appended', 'text-delta', 'thinking-delta', 'partial-tool-call', 'tool-call-started'].includes(update.type)) {
              turnEnded = false;
              stopQuiet();
            }
            if (update.type === 'text-delta') emit({ type: 'delta', kind: 'text', text: update.text });
            else if (update.type === 'thinking-delta') emit({ type: 'delta', kind: 'thinking', text: update.text });
          },
        };
        try { run = await agent.send(text, sendOptions); }
        catch (error) {
          // Same stale run as at start, if it appeared after: cancel it and retry once.
          if (!/already has active run/i.test(short(error)) || !(await cancelStale(agent.agentId).catch(() => false))) throw error;
          emit({ type: 'stale', agent_id: agent.agentId, action: 'cancelled' });
          run = await agent.send(text, sendOptions);
        }
        const current = run;
        last = await Promise.race([(async () => {
          for await (const event of current.stream()) {
            if (event.type === 'tool_call') emit({ type: 'tool', call_id: event.call_id, name: event.name, status: event.status, args: event.args, result: event.result });
            else if (event.type === 'usage') emit({ type: 'usage', usage: event.usage });
          }
          return current.wait();
        })(), gaveUp]);
        stopQuiet();
        if (turnEnded && !hasWork() && last.status !== 'finished' && (quietEnded || /repeated resume attempts made no progress/i.test(short(last.error)))) {
          if (!quietEnded) emit({ type: 'turn_end_settled', action: 'no_progress', status: last.status });
          last = { ...last, status: 'finished', error: undefined };
        }
        run = undefined;
        if (last.status !== 'finished') break;
        let timer;
        await Promise.race([Promise.all(steers.map(entry => entry.done)), new Promise(resolve => { timer = setTimeout(resolve, 15000); })]);
        clearTimeout(timer);
        for (const entry of steers) settle(entry, 'unconfirmed');
        steers = [];
        text = followups.shift();
      }
      emit({ type: 'result', status: last.status, result: last.result || '', error: last.error ? short(last.error) : undefined, usage: last.usage });
    } catch (error) {
      emit({ type: 'result', status: 'error', error: short(error) });
    } finally { busy = false; run = undefined; drop(); }
  }
  await agent?.[Symbol.asyncDispose]?.();
}
main().then(() => process.exit(0), error => { emit({ type: 'fatal', error: short(error) }); process.exit(1); });
