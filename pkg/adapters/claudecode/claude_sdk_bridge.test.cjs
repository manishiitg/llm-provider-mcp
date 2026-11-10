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
export function query({ prompt }) {
  const iterator = (async function* () {
    const first = await prompt.next();
    yield { type: 'system', subtype: 'init', session_id: 'native-test' };
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
  assert.deepEqual(events.filter(event => event.type === 'sdk_helper').map(event => event.helper.state), ['started', 'finished']);
  assert.ok(events.some(event => event.type === 'sdk_control_result' && event.id === 'new-direction' && !event.is_error));
  const result = events.filter(event => event.type === 'result').at(-1);
  assert.equal(result.result, 'Parser reviewed with your steer');
  assert.equal(result.sdk_continues, false);
  assert.notEqual(result.sdk_parked_turn, true);
});
