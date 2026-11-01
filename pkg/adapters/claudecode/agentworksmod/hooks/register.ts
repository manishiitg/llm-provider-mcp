import type { Register } from 'claude-code'

// AgentWorks runs this mod inside a chat's Claude Code terminal. It reports
// what the terminal does as one small JSON file per event in
// $AGENTWORKS_MOD_DIR/events, which AgentWorks reads in order. Without that
// folder (a person's own Claude Code) it does nothing.

let seq = 0

async function emit($: any, type: string, fields: Record<string, unknown> = {}) {
  const dir = await $.env.get('AGENTWORKS_MOD_DIR')
  if (!dir) return
  seq += 1
  const at = Date.now()
  const name = `${String(at).padStart(14, '0')}-${String(seq).padStart(6, '0')}-${type}.json`
  const sessionId = await $.session.id()
  await $.fs.write(`${dir}/events/${name}`, JSON.stringify({ type, at, seq, session_id: sessionId, ...fields }))
}

export const register: Register = on => {
  on('session.start', async ($, e, next) => {
    const result = await next(e)
    await emit($, 'session')
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
    if (!e.agentId) await emit($, 'turn_start', { turn_id: e.turnId })
    return next(e)
  })

  on('turn.complete', async ($, e, next) => {
    const result = await next(e)
    if (!(e as any).agentId) {
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
