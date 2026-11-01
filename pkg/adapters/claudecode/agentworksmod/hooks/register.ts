import type { Register } from 'claude-code'

// AgentWorks runs this mod inside a chat's Claude Code terminal. It reports
// what the terminal does as one small JSON file per event in
// $AGENTWORKS_MOD_DIR/events, which AgentWorks reads in order. Without that
// folder (a person's own Claude Code) it does nothing.

let seq = 0
// The main conversation is answering: input then joins the running turn.
let running = false
let turnId = ''
let delivering = false
let toolsReady = false
const delivered = new Set<string>()
// Streamed pieces of the main conversation's current model request, written
// out about every 150 ms; stepKey names that request for its stored rows.
let stream: Record<string, unknown>[] = []
let flushedAt = 0
let stepKey = ''
// Rows the chat history keeps: prompts, answer blocks, tool results, input
// joined into a running turn.
const ROW_DOORS = new Set(['prompt', 'response', 'tool-result', 'delivery', 'note'])
const ROW_TEXT_LIMIT = 200000

async function emit($: any, type: string, fields: Record<string, unknown> = {}) {
  // Numbered before any await, so concurrent events keep their order.
  const n = ++seq
  const at = Date.now()
  const dir = await $.env.get('AGENTWORKS_MOD_DIR')
  if (!dir) return
  const name = `${String(at).padStart(14, '0')}-${String(n).padStart(6, '0')}-${type}.json`
  const sessionId = await $.session.id()
  await $.fs.write(`${dir}/events/${name}`, JSON.stringify({ type, at, seq: n, session_id: sessionId, ...fields }))
}

// deliverInbox hands Claude the messages AgentWorks put in $AGENTWORKS_MOD_DIR/inbox,
// in name order, once each: a new turn when Claude is idle, or a row the
// running turn reads at its next step. Each is confirmed with an "input" event.
async function deliverInbox($: any) {
  if (delivering) return
  delivering = true
  try {
    const dir = await $.env.get('AGENTWORKS_MOD_DIR')
    if (!dir) return
    const entries = (await $.fs.list(`${dir}/inbox`).catch(() => [])) as { name: string; kind: string }[]
    const names = entries.filter(entry => entry.kind === 'file' && entry.name.endsWith('.json')).map(entry => entry.name).sort()
    // A stop goes first: a message queued behind it must not hold it up.
    names.sort((a, b) => Number(b.includes('-abort')) - Number(a.includes('-abort')))
    for (const name of names) {
      if (delivered.has(name)) continue
      delivered.add(name)
      const request = JSON.parse(await $.fs.read(`${dir}/inbox/${name}`))
      const id = String(request.id ?? name)
      try {
        await emit($, 'input', { input_id: id, ...(await handle($, request)) })
      } catch (err) {
        await emit($, 'input', { input_id: id, error: String(err) })
      }
    }
  } finally {
    delivering = false
  }
}

async function flushStream($: any, force: boolean) {
  if (stream.length === 0 || (!force && Date.now() - flushedAt < 150)) return
  const items = stream
  stream = []
  flushedAt = Date.now()
  await emit($, 'stream', { step: stepKey, items })
}

// trimRow keeps a stored row small enough for one event file: long text and
// tool output are cut, with a note of how much.
function trimRow(message: any): any {
  const cut = (text: string) => (text.length > ROW_TEXT_LIMIT ? `${text.slice(0, ROW_TEXT_LIMIT)}\n[cut: ${text.length - ROW_TEXT_LIMIT} more characters]` : text)
  const content = Array.isArray(message?.content)
    ? message.content.map((block: any) => {
        if (block?.type === 'text' && typeof block.text === 'string') return { ...block, text: cut(block.text) }
        if (block?.type === 'tool_result') {
          if (typeof block.content === 'string') return { ...block, content: cut(block.content) }
          if (Array.isArray(block.content)) return { ...block, content: block.content.map((part: any) => (part?.type === 'text' ? { ...part, text: cut(String(part.text ?? '')) } : { type: 'text', text: `[${part?.type ?? 'item'} not shown]` })) }
        }
        if (block?.type === 'image' || block?.type === 'document') return { type: 'text', text: `[${block.type} not shown]` }
        return block
      })
    : message?.content
  return { role: message?.role, content }
}

// waitForMcpTools holds the first message until the chat's MCP servers
// (AGENTWORKS_MOD_MCP_SERVERS, comma separated) have listed their tools, at
// most 30 s: they connect after the session starts, and a turn sent before
// them runs without their tools.
async function waitForMcpTools($: any) {
  if (toolsReady) return
  const normalize = (name: string) => name.replace(/[^A-Za-z0-9_-]/g, '_')
  const want = String((await $.env.get('AGENTWORKS_MOD_MCP_SERVERS')) ?? '').split(',').map(name => normalize(name.trim())).filter(Boolean)
  const deadline = Date.now() + 30000
  while (want.length > 0 && Date.now() < deadline) {
    const tools = (await $.tool.list()) as { name: string; mcp: boolean }[]
    const servers = new Set(tools.filter(tool => tool.mcp).map(tool => tool.name.split('__')[1]))
    if (want.every(name => servers.has(name))) break
    await $.clock.sleep(250)
  }
  toolsReady = true
}

// handle runs one inbox request and says how it went.
async function handle($: any, request: { kind?: string; text?: string }): Promise<Record<string, unknown>> {
  switch (request.kind ?? 'input') {
    case 'abort':
      if (!running || !turnId) return { delivery: 'idle' }
      await $.turn.abort({ turnId })
      return { delivery: 'aborted' }
    case 'compact': {
      if (running) throw new Error('a turn is running')
      const compacted = await $.session.compact()
      if (compacted.skip) return { delivery: 'skipped', reason: String(compacted.skip) }
      return { delivery: 'compacted', tokens_before: compacted.tokensBefore ?? 0, tokens_after: compacted.tokensAfter ?? 0 }
    }
    default: {
      await waitForMcpTools($)
      const text = String(request.text ?? '')
      if (running) {
        const stored = await $.session.append({ message: { type: 'user', content: [{ type: 'text', text }] } })
        if (stored.deny) throw new Error(String(stored.deny))
        return { delivery: 'running_turn' }
      }
      await $.prompt.submit({ text, asUser: true })
      return { delivery: 'new_turn' }
    }
  }
}

export const register: Register = on => {
  on('session.start', async ($, e, next) => {
    const result = await next(e)
    await emit($, 'session')
    $.clock.every(250, () => {
      void deliverInbox($)
    })
    return result
  })

  // /clear, /resume and /branch end this session; the engine names the one
  // it moves to, and the next turn's events carry its id as well.
  on('session.end', async ($, e, next) => {
    const result = await next(e)
    await emit($, 'session_end', { reason: e.reason, ended_session_id: e.sessionId, next_session_id: e.resume?.id ?? '' })
    return result
  })

  on('command.run', async ($, e, next) => {
    const result = await next(e)
    await emit($, 'command', { command: e.command })
    return result
  })

  on('turn.start', async ($, e, next) => {
    if (!e.agentId) {
      running = true
      turnId = e.turnId
      await emit($, 'turn_start', { turn_id: e.turnId })
    }
    return next(e)
  })

  // The main conversation's model requests, streamed as they arrive: text,
  // thinking, each tool call and its input as Claude writes it, and the stop.
  on('turn.step', async function* ($, e, next) {
    const main = !e.agentId
    if (main) stepKey = `${e.turnId}:${e.index}`
    const chunks = next(e)
    while (true) {
      const step = await chunks.next()
      if (step.done) {
        if (main) await flushStream($, true)
        return step.value
      }
      const chunk: any = step.value
      if (main) {
        switch (chunk.kind) {
          case 'text':
          case 'thinking':
            stream.push({ kind: chunk.kind, index: chunk.index, text: chunk.text })
            break
          case 'tool':
            stream.push({ kind: 'tool', index: chunk.index, id: chunk.id, name: chunk.name })
            break
          case 'input':
            stream.push({ kind: 'input', index: chunk.index, json: chunk.json })
            break
          case 'stop':
            stream.push({ kind: 'stop', stop_reason: chunk.stopReason, usage: chunk.usage })
            break
        }
        await flushStream($, chunk.kind === 'stop' || chunk.kind === 'tool')
      }
      yield chunk
    }
  })

  on('session.append', async ($, e, next) => {
    const stored: any = await next(e)
    if (!(e as any).agentId && ROW_DOORS.has(String((e as any).door)) && stored?.message) {
      await emit($, 'row', { door: (e as any).door, uuid: stored.uuid, step: stepKey, message: trimRow(stored.message) })
    }
    return stored
  })

  // Every compaction, whoever asked for it: /compact, Claude's own threshold, AgentWorks.
  on('session.compact', async ($, e, next) => {
    const result = await next(e)
    if (!result.skip) await emit($, 'compaction', { trigger: String((e as any).trigger ?? ''), tokens_before: result.tokensBefore ?? 0, tokens_after: result.tokensAfter ?? 0 })
    return result
  })

  on('turn.complete', async ($, e, next) => {
    const result = await next(e)
    if (!(e as any).agentId) {
      running = false
      const usage = await $.session.usage()
      await emit($, 'turn_complete', {
        turn_id: e.turnId,
        reason: e.reason,
        aborted: e.isAborted,
        answer: e.answer,
        duration_ms: e.durationMs,
        context_tokens: usage.context?.tokens ?? 0,
        context_window: usage.context?.window ?? 0,
        cost_usd: usage.cost?.usd ?? 0,
        rate_limits: usage.rateLimits ?? [],
      })
    }
    return result
  })
}
