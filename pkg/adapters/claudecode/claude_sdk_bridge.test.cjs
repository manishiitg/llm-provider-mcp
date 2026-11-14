const { test } = require('node:test');
const assert = require('node:assert/strict');
const { mkdtempSync, writeFileSync, rmSync } = require('node:fs');
const { tmpdir } = require('node:os');
const { join } = require('node:path');
const { spawn } = require('node:child_process');
const { createHelperTracker } = require('./claude_sdk_bridge.cjs');

test('PLAT-872 native helper bookends ignore shell/ambient tasks and late runs', () => {
  const events = [], tracker = createHelperTracker(event => events.push(event.helper));
  const system = (subtype, extra) => tracker.observe({ type: 'system', subtype, ...extra });
  system('background_tasks_changed', { tasks: [
    { task_id: 'shell', task_type: 'local_bash' },
    { task_id: 'watcher', task_type: 'local_agent', ambient: true },
    { task_id: 'helper', run_id: '1', task_type: 'local_agent', description: 'Check parser' },
  ] });
  assert.equal(tracker.running(), true);
  system('task_notification', { task_id: 'helper', run_id: '1', status: 'completed' });
  system('background_tasks_changed', { tasks: [{ task_id: 'helper', run_id: '1' }] });
  assert.equal(tracker.running(), false);
  system('task_started', { task_id: 'helper', run_id: '2', task_type: 'local_agent' });
  system('task_notification', { task_id: 'helper', run_id: '1', status: 'failed' });
  assert.equal(tracker.running(), true);
  system('task_progress', { task_id: 'helper', run_id: '2' });
  system('task_notification', { task_id: 'helper', run_id: '2', status: 'stopped' });
  assert.deepEqual(events.map(event => event.state), ['started', 'finished', 'started', 'progress', 'interrupted']);
});

test('PLAT-872 async Agent without run_in_background survives parent result, accepts steer, and delivers completion', { timeout: 5000 }, async t => {
  const folder = mkdtempSync(join(tmpdir(), 'sdk-helper-'));
  t.after(() => rmSync(folder, { recursive: true, force: true }));
  // Same official query interface and native messages; no model or credentials.
  writeFileSync(join(folder, 'sdk.mjs'), `
export function query({ prompt, options }) {
  if (options.thinking?.type !== 'adaptive' || options.thinking?.display !== 'summarized' || options.extraArgs?.['thinking-display'] !== 'summarized') throw new Error('thinking summaries not requested');
  const iterator = (async function* () {
    const first = await prompt.next();
    yield { type: 'system', subtype: 'init', session_id: 'native-test' };
    yield { type: 'stream_event', event: { type: 'content_block_delta', delta: { type: 'thinking_delta', thinking: 'Checking the parser.' } } };
    yield { type: 'assistant', message: { content: [{ type: 'tool_use', id: 'agent-tool', name: 'Agent', input: { description: 'Check parser' } }] } };
    yield { type: 'system', subtype: 'task_started', task_type: 'local_agent', task_id: 'helper', run_id: '1', tool_use_id: 'agent-tool', description: 'Check parser' };
    yield { type: 'result', result: 'Helper is working', user_message_uuids: [first.value.uuid] };
    const steer = await prompt.next();
    if (steer.done) throw new Error('parent closed before helper completion');
    yield { type: 'user', uuid: steer.value.uuid };
    yield { type: 'system', subtype: 'task_notification', task_id: 'helper', run_id: '1', status: 'completed' };
    yield { type: 'result', result: 'Parser reviewed with your steer', user_message_uuids: [steer.value.uuid] };
  })();
  iterator.close = () => {};
  return iterator;
}`);
  const child = spawn(process.execPath, [join(__dirname, 'claude_sdk_bridge.cjs'), join(folder, 'sdk.mjs'), 'unused'], { stdio: ['pipe', 'pipe', 'pipe'], env: { ...process.env, AGENTWORKS_CLI_LAUNCH: '' } });
  t.after(() => child.kill());
  const events = []; let pending = '', stderr = '';
  child.stderr.on('data', data => stderr += data);
  child.stdout.on('data', data => {
    pending += data;
    while (pending.includes('\n')) {
      const end = pending.indexOf('\n'), line = pending.slice(0, end); pending = pending.slice(end + 1);
      const event = JSON.parse(line); events.push(event);
      if (event.type === 'result' && event.result === 'Helper is working') {
        assert.equal(event.sdk_continues, true);
        child.stdin.write(JSON.stringify({ type: 'steer', id: 'new-direction', text: 'Also check errors' }) + '\n');
      }
    }
  });
  const finished = new Promise((resolve, reject) => { child.once('error', reject); child.once('exit', code => code === 0 ? resolve() : reject(new Error(stderr || 'bridge failed'))); });
  child.stdin.write(JSON.stringify({ type: 'prompt', text: 'Delegate parser review' }) + '\n');
  await finished;
  assert.ok(events.some(event => event.type === 'stream_event' && event.event?.delta?.thinking === 'Checking the parser.'));
  assert.deepEqual(events.filter(event => event.type === 'sdk_helper').map(event => event.helper.state), ['started', 'finished']);
  assert.ok(events.some(event => event.type === 'sdk_control_result' && event.id === 'new-direction' && !event.is_error));
  const result = events.filter(event => event.type === 'result').at(-1);
  assert.equal(result.result, 'Parser reviewed with your steer');
  assert.equal(result.sdk_continues, false);
  assert.notEqual(result.sdk_parked_turn, true);
});

test('PLAT-746-899 Claude wakes itself after its background command; the host adopts that turn', { timeout: 5000 }, async t => {
  const folder = mkdtempSync(join(tmpdir(), 'sdk-wake-'));
  t.after(() => rmSync(folder, { recursive: true, force: true }));
  // The official SDK starts a turn on its own when a background task finishes (seen live 12 Nov).
  writeFileSync(join(folder, 'sdk.mjs'), `
export function query({ prompt }) {
  const iterator = (async function* () {
    const first = await prompt.next();
    yield { type: 'system', subtype: 'init', session_id: 'native-test' };
    yield { type: 'assistant', message: { content: [{ type: 'tool_use', id: 'bg-tool', name: 'Bash', input: { command: 'sleep 25', run_in_background: true } }] } };
    yield { type: 'system', subtype: 'background_tasks_changed', tasks: [{ task_id: 'bg', run_id: '1', tool_use_id: 'bg-tool' }] };
    yield { type: 'result', result: 'Started it', user_message_uuids: [first.value.uuid] };
    yield { type: 'system', subtype: 'task_notification', task_id: 'bg', run_id: '1', status: 'completed' };
    yield { type: 'system', subtype: 'background_tasks_changed', tasks: [] };
    yield { type: 'assistant', message: { content: [{ type: 'text', text: 'Its output was DONE' }] } };
    await new Promise(resolve => setTimeout(resolve, 200));
    yield { type: 'result', result: 'Its output was DONE' };
  })();
  iterator.close = () => {};
  return iterator;
}`);
  const child = spawn(process.execPath, [join(__dirname, 'claude_sdk_bridge.cjs'), join(folder, 'sdk.mjs'), 'unused'], { stdio: ['pipe', 'pipe', 'pipe'], env: { ...process.env, AGENTWORKS_CLI_LAUNCH: '' } });
  t.after(() => child.kill());
  const events = []; let pending = '', stderr = '';
  child.stderr.on('data', data => stderr += data);
  child.stdout.on('data', data => {
    pending += data;
    while (pending.includes('\n')) {
      const end = pending.indexOf('\n'), line = pending.slice(0, end); pending = pending.slice(end + 1);
      const event = JSON.parse(line); events.push(event);
      // The host sees Claude's own turn start and adopts it, as the Go session does.
      if (event.type === 'assistant' && event.sdk_parked_turn) child.stdin.write(JSON.stringify({ type: 'adopt' }) + '\n');
    }
  });
  const finished = new Promise((resolve, reject) => { child.once('error', reject); child.once('exit', code => code === 0 ? resolve() : reject(new Error(stderr || 'bridge failed'))); });
  child.stdin.write(JSON.stringify({ type: 'prompt', text: 'Run sleep 25 in the background' }) + '\n');
  await finished;
  const results = events.filter(event => event.type === 'result');
  assert.equal(results[0].sdk_parked, true);
  // Adopted: the wake's result is the host's turn (not dropped as a parked turn), and with no task left the session ends.
  assert.equal(results.at(-1).result, 'Its output was DONE');
  assert.notEqual(results.at(-1).sdk_parked_turn, true);
  assert.equal(results.at(-1).sdk_parked, undefined);
});

// PLAT-746-941: preserve per-task reports; a shell command has no invented
// model count and must not change the existing foreground hold decision.
test('background task progress retains only its own usage and real bookends', () => {
  const updates = [], tasks = createHelperTracker(event => updates.push(event.helper), true);
  const send = (subtype, task_id, extra = {}) => tasks.observe({ type: 'system', subtype, task_id, run_id: '1', ...extra });
  tasks.observe({ type: 'assistant', message: { content: [{ type: 'tool_use', id: 'shell-tool', name: 'Bash', input: { command: 'deploy.sh verify', run_in_background: true } }] } });
  send('task_started', 'shell', { task_type: 'local_bash', tool_use_id: 'shell-tool' });
  assert.equal(tasks.running(), false);
  send('task_started', 'agent-a', { task_type: 'local_agent' });
  send('task_started', 'agent-b', { task_type: 'local_agent' });
  send('task_progress', 'agent-a', { usage: { total_tokens: 18400, duration_ms: 134000 } });
  send('task_progress', 'agent-b', { usage: { total_tokens: 500, duration_ms: 1000 } });
  send('task_notification', 'agent-a', { status: 'completed', usage: { total_tokens: 24100, duration_ms: 182000 } });
  send('task_notification', 'shell', { status: 'completed' });
  assert.equal(updates.findLast(task => task.task_id === 'agent-a').total_tokens, 24100);
  assert.equal(updates.findLast(task => task.task_id === 'agent-b').total_tokens, 500);
  assert.equal(updates.findLast(task => task.task_id === 'shell').total_tokens, undefined);
  assert.equal(updates.findLast(task => task.task_id === 'shell').state, 'finished');
  assert.equal(tasks.running(), true); // agent-b, not the finished shell
});

// Ordinary CLI-managed Bash ids must not become extra background rows.
test('shell rows require explicit background or an observed timeout', () => {
  for (const mode of ['ordinary', 'explicit', 'auto']) {
    const updates = [], tracker = createHelperTracker(e => updates.push(e.helper), true);
    tracker.observe({ type: 'assistant', message: { content: [{ type: 'tool_use', id: 'bash', name: 'Bash', input: { command: 'deploy.sh verify', run_in_background: mode === 'explicit' } }] } });
    tracker.observe({ type: 'user', toolUseResult: { backgroundTaskId: 'bg', ...(mode === 'auto' ? { timedOutAfterMs: 1000 } : {}) }, message: { content: [{ type: 'tool_result', tool_use_id: 'bash' }] } });
    tracker.observe({ type: 'system', subtype: 'task_started', task_type: 'local_bash', tool_use_id: 'bash', task_id: 'bg', run_id: '1' });
    tracker.observe({ type: 'system', subtype: 'task_notification', task_type: 'local_bash', tool_use_id: 'bash', task_id: 'bg', run_id: '1', status: 'completed' });
    assert.equal(updates.length, mode === 'ordinary' ? 0 : 2);
    for (const task of updates) { assert.equal(task.command, 'deploy.sh verify'); assert.equal(task.background_mode, mode); }
    assert.equal(tracker.running(), false);
  }
});

test('helper type and description follow task-owned live progress without a poller', () => {
  const updates = [], tracker = createHelperTracker(e => updates.push(e.helper), true);
  tracker.observe({ type: 'assistant', message: { content: [{ type: 'tool_use', id: 'explore-tool', name: 'Agent', input: { subagent_type: 'Explore', description: 'Find routing code', run_in_background: true } }] } });
  tracker.observe({ type: 'system', subtype: 'task_started', task_id: 'explore', tool_use_id: 'explore-tool', task_type: 'local_agent', run_id: '1' });
  tracker.observe({ type: 'system', subtype: 'task_progress', task_id: 'explore', tool_use_id: 'explore-tool', run_id: '1', description: 'Grepping coding.go', usage: { total_tokens: 154200, duration_ms: 288000 } });
  const latest = updates.at(-1);
  assert.equal(latest.helper_type, 'Explore');
  assert.equal(latest.helper_description, 'Find routing code');
  assert.equal(latest.current_step, 'Grepping coding.go');
  assert.equal(latest.total_tokens, 154200);
});
