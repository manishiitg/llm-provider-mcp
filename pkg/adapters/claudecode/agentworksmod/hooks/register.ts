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
const delivered = new Set<string>()

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
