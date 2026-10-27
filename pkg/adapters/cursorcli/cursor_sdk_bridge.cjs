// Thin transport for the official Cursor SDK (local runtime). Policy and accounting stay in Go.
// One bridge per chat: it keeps the SDK agent between turns. Lines in: start, prompt, steer, cancel.
// Lines out: ready, delta, tool, usage, steer_accepted, result, fatal. The SDK logs to stdout
// too, so every protocol line carries a marker and Go ignores the rest.
const { createInterface } = require('node:readline');
const emit = value => process.stdout.write('\u0001aw ' + JSON.stringify(value) + '\n');
const short = error => String(error?.message || error || 'Error').split('\n')[0].slice(0, 300);

async function main() {
  const { Agent } = require(process.argv[2]);
  let agent, run, start, followups = [], busy = false;
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
    // Rules (system prompt) and MCP servers come from the .cursor files Go prepared; a bound folder's
    // servers also arrive explicitly in start.mcp_servers.
    local: { cwd: start.cwd, ...(start.dirs ? { dirs: start.dirs } : {}), settingSources: ['project', 'user'] },
    ...(start.mode ? { mode: start.mode } : {}),
    ...(start.tools ? { tools: start.tools } : {}),
    ...(start.mcp_servers ? { mcpServers: start.mcp_servers } : {}),
  });
  // Steering and cancel arrive while a run streams, so they are handled as they are read.
  const control = request => {
    if (request.type === 'steer') {
      if (!busy || !run) { emit({ type: 'steer_accepted', id: request.id, accepted: false }); return true; }
      emit({ type: 'steer_accepted', id: request.id, accepted: true });
      const steer = run.steer ? run.steer(request.text) : Promise.resolve('revert_to_followup');
      steer.then(outcome => {
        // Not taken by the running turn: answer it in this same host turn, right after.
        if (outcome !== 'complete_delivered') followups.push(request.text);
      }, () => followups.push(request.text));
      return true;
    }
    if (request.type === 'cancel') { followups = []; run?.cancel().catch(() => {}); return true; }
    return false;
  };
  lines.on('line', line => {
    const request = JSON.parse(line);
    if (busy && control(request)) return;
    queue.push(request); wake?.();
  });
  lines.on('close', () => { queue.push({ type: 'close' }); wake?.(); });
  for (;;) {
    const request = await next();
    if (request.type === 'close') break;
    if (control(request)) continue;
    if (request.type === 'start') {
      start = request;
      if (start.agent_id) {
        try { agent = await Agent.resume(start.agent_id, options()); }
        catch (error) {
          // A saved agent this store does not have: start fresh and let Go send the history.
          if (!/not.?found|unknown agent|does not exist/i.test(short(error))) throw error;
          agent = undefined;
        }
      }
      const resumed = Boolean(agent);
      if (!agent) agent = await Agent.create(options());
      emit({ type: 'ready', agent_id: agent.agentId, resumed });
      continue;
    }
    if (request.type !== 'prompt') continue;
    busy = true;
    let text = request.text, last;
    try {
      while (text !== undefined) {
        run = await agent.send(text, {
          onDelta: ({ update }) => {
            if (update.type === 'text-delta') emit({ type: 'delta', kind: 'text', text: update.text });
            else if (update.type === 'thinking-delta') emit({ type: 'delta', kind: 'thinking', text: update.text });
          },
        });
        for await (const event of run.stream()) {
          if (event.type === 'tool_call') emit({ type: 'tool', call_id: event.call_id, name: event.name, status: event.status, args: event.args, result: event.result });
          else if (event.type === 'usage') emit({ type: 'usage', usage: event.usage });
        }
        last = await run.wait();
        run = undefined;
        if (last.status !== 'finished') break;
        text = followups.shift();
      }
      emit({ type: 'result', status: last.status, result: last.result || '', error: last.error ? short(last.error) : undefined, usage: last.usage });
    } catch (error) {
      emit({ type: 'result', status: 'error', error: short(error) });
    } finally { busy = false; run = undefined; followups = []; }
  }
  await agent?.[Symbol.asyncDispose]?.();
}
main().then(() => process.exit(0), error => { emit({ type: 'fatal', error: short(error) }); process.exit(1); });
