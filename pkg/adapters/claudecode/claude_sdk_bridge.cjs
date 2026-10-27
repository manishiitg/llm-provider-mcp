// Thin transport for the official Anthropic Agent SDK. Policy and accounting stay in Go.
const { createInterface } = require('node:readline');
const { pathToFileURL } = require('node:url');
const { randomUUID } = require('node:crypto');
const { spawn } = require('node:child_process');
const { writeFileSync } = require('node:fs');
const { tmpdir } = require('node:os');
const { join } = require('node:path');
const emit = value => process.stdout.write(JSON.stringify(value) + '\n');
let nativeErrorReported = false;
const deny = () => ({ behavior: 'deny', message: 'The request was cancelled. Use the chat or account setup to continue.' });

async function main() {
  const [sdkEntry, executable, ...argv] = process.argv.slice(2);
  const { query } = await import(pathToFileURL(sdkEntry).href);
  const options = {
    pathToClaudeCodeExecutable: executable,
    cwd: process.cwd(), env: process.env,
    includePartialMessages: true,
    // Match the CLI's normal Claude Code instructions and project skills.
    systemPrompt: { type: 'preset', preset: 'claude_code' },
    settingSources: ['user', 'project', 'local'],
    extraArgs: { 'replay-user-messages': null },
  };
  // A confined chat: this bridge runs outside Landlock and every Claude Code
  // process the SDK starts is exec'd in place by the launcher, so it keeps its
  // own /proc entry (PLAT-872-51). The launcher deletes the policy file it reads.
  const launch = process.env.AGENTWORKS_CLI_LAUNCH && JSON.parse(process.env.AGENTWORKS_CLI_LAUNCH);
  delete process.env.AGENTWORKS_CLI_LAUNCH;
  if (launch) {
    options.spawnClaudeCodeProcess = ({ command, args, cwd, env, signal }) => {
      const policy = join(tmpdir(), `agentworks-cli-landlock-${randomUUID()}.json`);
      writeFileSync(policy, JSON.stringify(launch.policy), { mode: 0o600 });
      return spawn(launch.runner, ['--config', policy, '--', ...launch.env_args, command, ...args],
        { cwd, env, signal, stdio: ['pipe', 'pipe', 'pipe'] });
    };
  }
  for (let i = 0; i < argv.length; i++) {
    const flag = argv[i];
    if (flag === '-p' || flag === '--verbose') continue;
    if (flag === '--output-format') { i++; continue; }
    if (flag === '--dangerously-skip-permissions') {
      options.permissionMode = 'bypassPermissions'; options.allowDangerouslySkipPermissions = true; continue;
    }
    if (flag === '--strict-mcp-config') { options.strictMcpConfig = true; continue; }
    const value = argv[++i];
    switch (flag) {
      case '--model': options.model = value; break;
      case '--effort': options.effort = value; break;
      case '--append-system-prompt': options.systemPrompt.append = value; break;
      case '--permission-mode': options.permissionMode = value; if (value === 'bypassPermissions') options.allowDangerouslySkipPermissions = true; break;
      case '--resume': options.resume = value; break;
      case '--session-id': options.sessionId = value; break;
      case '--tools': options.tools = value ? value.split(',') : []; break;
      case '--allowedTools': options.allowedTools = value.split(','); break;
      case '--disallowedTools': options.disallowedTools = [...(options.disallowedTools || []), ...value.split(',')]; break;
      case '--add-dir': (options.additionalDirectories ||= []).push(value); break;
      case '--settings': options.settings = value; break;
      case '--mcp-config': options.extraArgs['mcp-config'] = value; break;
      default: throw new Error(`Unsupported SDK launch flag ${flag}`);
    }
  }
  let run, wake, ended = false;
  const input = [], permissions = new Map(), steering = new Map(), unsettled = new Set(), background = new Map();
  const lines = createInterface({ input: process.stdin });
  const finishInput = () => { ended = true; wake?.(); };
  const cancelPermissions = () => { for (const resolve of permissions.values()) resolve(deny()); permissions.clear(); };
  options.canUseTool = async (tool, toolInput, context) => {
    const id = randomUUID();
    const abort = () => { permissions.get(id)?.(deny()); permissions.delete(id); emit({ type: 'sdk_permission_cancel', request_id: id }); };
    if (context.signal.aborted) return deny();
    const response = new Promise(resolve => permissions.set(id, resolve));
    context.signal.addEventListener('abort', abort, { once: true });
    emit({ type: 'sdk_permission', request_id: id, tool_name: tool, input: toolInput });
    try { return await response; }
    finally { context.signal.removeEventListener('abort', abort); permissions.delete(id); }
  };
  const first = new Promise((resolve, reject) => {
    lines.on('line', line => {
      try {
        const request = JSON.parse(line);
        if (request.type === 'prompt') { input.push({ text: request.text }); wake?.(); resolve(); }
        else if (request.type === 'steer') {
          if (!run || ended) {
            emit({ type: 'sdk_control_result', id: request.id, is_error: true, input_not_sent: true });
          } else {
            const uuid = randomUUID();
            steering.set(uuid, request.id);
            unsettled.add(uuid);
            input.push({ text: request.text, uuid, priority: 'now' });
            wake?.();
          }
        }
        else if (request.type === 'permission') permissions.get(request.id)?.(request.response);
        else if (request.type === 'interrupt' && run) {
          cancelPermissions();
          run.interrupt().then(() => { unsettled.clear(); background.clear(); finishInput(); emit({ type: 'sdk_control_result', id: request.id }); }, () => emit({ type: 'sdk_control_result', id: request.id, is_error: true }));
        }
      } catch (error) { reject(error); }
    });
    lines.on('close', () => { finishInput(); cancelPermissions(); resolve(); });
  });
  await first;
  async function* messages() {
    while (!ended || input.length) {
      if (!input.length) { await new Promise(resolve => { wake = resolve; }); wake = undefined; continue; }
      const next = input.shift();
      // Native human priority-now delivery lets movable tools step aside while
      // the running conversation takes the new direction (PLAT-872-47).
      yield { type: 'user', message: { role: 'user', content: next.text }, parent_tool_use_id: null,
        uuid: next.uuid, priority: next.priority, origin: { kind: 'human' } };
    }
  }
  run = query({ prompt: messages(), options });
  try {
    for await (const event of run) {
      // Keep work moved aside by steering alive until its native completion.
      // An empty roster can precede task_notification, so only that terminal
      // notification retires a known task. Ambient watchers do not hold a turn.
      if (event.type === 'system' && event.subtype === 'background_tasks_changed') {
        for (const task of event.tasks || []) {
          if (!task.ambient) background.set(task.task_id, task.run_id);
        }
      }
      if (event.type === 'system' && event.subtype === 'task_notification' &&
          background.get(event.task_id) === event.run_id) background.delete(event.task_id);
      if (event.type === "result" && event.is_error) nativeErrorReported = true;
      const consumed = new Set([...(event.user_message_uuids || []), event.user_message_uuid]);
      if (event.type === 'user' && !event.parent_tool_use_id) consumed.add(event.uuid);
      for (const uuid of consumed) {
        if (steering.has(uuid)) {
          emit({ type: 'sdk_control_result', id: steering.get(uuid) });
          steering.delete(uuid);
        }
      }
      if (event.type === 'result') {
        for (const uuid of consumed) unsettled.delete(uuid);
        event.sdk_continues = !ended && (unsettled.size > 0 || background.size > 0 || event.queued_turn_count > 0);
      }
      emit(event);
      // One host turn can consume several steers. Close only after their native
      // results, then drain transcript writes before the next host resumes.
      if (event.type === 'result' && !event.sdk_continues) finishInput();
    }
  } finally { finishInput(); cancelPermissions(); lines.close(); run.close(); }
}
main().then(() => process.stdout.write("", () => process.exit(0)), error => {
  // SDK errors can contain command-line/environment details: report only a
  // stable category here. Native semantic errors still arrive as result events.
  if (!nativeErrorReported) emit({ type: 'result', is_error: true, result: `Claude Agent SDK failed (${error?.name || 'Error'}). Check SDK installation and the selected Claude login.` });
  process.exitCode = 1;
  process.stdin.destroy();
});
