// Per-invocation Mitos extension. Never writes the operator's Pi settings.
// Duties: deliver Mitos attention into the session, gate tools at breakpoints,
// execute the project retry policy for transient model-provider failures, and
// report model status and usage back to Mitos.
export function parseHTTPCode(message) {
  const head = String(message || '').slice(0, 120);
  const patterns = [/^\s*(\d{3})\b/, /\berror\s*\(\s*(\d{3})\s*\)/i, /\b(?:http|status)[\s_-]*(\d{3})\b/i];
  for (const pattern of patterns) {
    const match = pattern.exec(head);
    const code = match ? Number(match[1]) : 0;
    if (code >= 100 && code <= 599) return code;
  }
  return 0;
}

export function parseRetryAfterMs(message) {
  const match = /retry[-_ ]?after["']?\s*[:=]\s*"?(\d{1,8})/i.exec(message || '');
  return match ? Number(match[1]) * 1000 : 0;
}

export const defaultPolicy = () => ({
  transient_codes: [408, 425, 429, 500, 502, 503, 504],
  max_attempts: 3,
  backoff_seconds: 5,
  backoff_multiplier: 2,
  max_backoff_seconds: 60,
  retry_after_cap_seconds: 600,
  fallback_models: [],
});

export function normalizePolicy(raw) {
  const d = defaultPolicy();
  const p = { ...d, ...(raw || {}) };
  if (!Array.isArray(p.transient_codes) || !p.transient_codes.length) p.transient_codes = d.transient_codes;
  if (!Array.isArray(p.fallback_models)) p.fallback_models = [];
  for (const key of ['max_attempts', 'backoff_seconds', 'max_backoff_seconds', 'retry_after_cap_seconds']) {
    if (!Number.isFinite(p[key]) || p[key] < 1) p[key] = d[key];
  }
  if (!Number.isFinite(p.backoff_multiplier) || p.backoff_multiplier < 1) p.backoff_multiplier = d.backoff_multiplier;
  return p;
}

export function backoffMs(policy, attemptsMade) {
  const growth = Math.pow(policy.backoff_multiplier, Math.max(0, attemptsMade - 1));
  return Math.min(policy.backoff_seconds * 1000 * growth, policy.max_backoff_seconds * 1000);
}

// decide maps one failed model call to the next recovery action. attemptsMade
// counts failed attempts on the current model, including the original call.
export function decide(policy, failure) {
  if (!policy.transient_codes.includes(failure.code)) return { action: 'surface', reason: failure.code ? 'non-transient' : 'unclassified' };
  if (failure.attemptsMade < policy.max_attempts) {
    const requested = failure.retryAfterMs
      ? Math.min(failure.retryAfterMs, policy.retry_after_cap_seconds * 1000)
      : backoffMs(policy, failure.attemptsMade);
    return { action: 'retry', delayMs: requested };
  }
  if (failure.fallbackIndex < policy.fallback_models.length) {
    return { action: 'switch', model: policy.fallback_models[failure.fallbackIndex] };
  }
  return { action: 'surface', reason: 'exhausted' };
}

export default function (pi) {
  const base = process.env.MITOS_URL, session = process.env.MITOS_SESSION_ID;
  if (!base || !session) return;
  let rawPolicy = {};
  try { rawPolicy = JSON.parse(process.env.MITOS_RETRY_POLICY || '{}'); } catch { rawPolicy = {}; }
  const policy = normalizePolicy(rawPolicy);
  let timer, stopped = false, busy = false, notices = [], seen = new Set();
  let failure = null;         // {code, message, attemptsMade, retryAfterMs, fallbackIndex}
  let retryTimer = null;      // scheduled recovery timer
  let usageByModel = {};      // cumulative per model: {input, cacheRead, cacheWrite, output}
  const request = async (path, body) => {
    const r = await fetch(base + path, { method: body === undefined ? 'GET' : 'POST', headers: { 'Content-Type': 'application/json', 'Mitos-API-Version': '1' }, body: body === undefined ? undefined : JSON.stringify(body), signal: AbortSignal.timeout(4000) });
    if (!r.ok) throw new Error('Mitos attention unavailable'); return r.json();
  };
  const report = (body) => request(`/api/sessions/${session}/agent-status`, body).catch(() => {});
  const currentModel = (ctx) => ctx && ctx.model ? `${ctx.model.provider}/${ctx.model.id}` : '';
  const usageBody = () => {
    const entries = Object.entries(usageByModel);
    if (!entries.length) return {};
    const usage = {};
    for (const [model, v] of entries) usage[model] = { input_tokens: v.input, cached_input_tokens: v.cacheRead, cache_write_tokens: v.cacheWrite, output_tokens: v.output };
    return { usage };
  };


  function clearFailure() {
    if (retryTimer) { clearTimeout(retryTimer); retryTimer = null; }
    failure = null;
  }

  async function recover(ctx) {
    const decision = decide(policy, failure);
    if (decision.action === 'retry') {
      const next = failure.attemptsMade + 1;
      report({ state: 'retry_scheduled', error: failure.message, http_code: failure.code, attempt: next, max_attempts: policy.max_attempts, model: currentModel(ctx), retry_in_seconds: Math.round(decision.delayMs / 1000), ...usageBody() });
      if (retryTimer) clearTimeout(retryTimer);
      const pending = failure;
      retryTimer = setTimeout(() => {
        retryTimer = null;
        if (stopped || !failure || failure !== pending) return;
        if (!ctx.isIdle()) { clearFailure(); return; } // the operator took over
        pi.sendMessage({ customType: 'mitos-retry', content: `Automatic retry ${next} of ${policy.max_attempts} after a transient model-provider failure (HTTP ${failure.code}). Continue the interrupted work from where the last attempt stopped; do not restart completed steps.`, display: true }, { deliverAs: 'steer', triggerTurn: true });
      }, decision.delayMs);
      return;
    }
    if (decision.action === 'switch') {
      // Split at the first slash only: model ids may themselves contain
      // slashes (openrouter/anthropic/claude-sonnet).
      const cut = String(decision.model).indexOf('/');
      const provider = cut > 0 ? String(decision.model).slice(0, cut) : '';
      const id = cut > 0 ? String(decision.model).slice(cut + 1) : '';
      const model = provider && id && ctx.modelRegistry ? ctx.modelRegistry.find(provider, id) : undefined;
      if (model && await pi.setModel(model)) {
        report({ state: 'model_switched', error: failure.message, http_code: failure.code, attempt: 0, max_attempts: policy.max_attempts, model: currentModel(ctx), next_model: decision.model, ...usageBody() });
        failure.attemptsMade = 0;
        failure.fallbackIndex += 1;
        retryTimer = setTimeout(() => {
          retryTimer = null;
          if (stopped || !failure || !ctx.isIdle()) { if (failure) clearFailure(); return; }
          pi.sendMessage({ customType: 'mitos-retry', content: `The previous model exhausted its retry budget, so Mitos switched this session to ${decision.model}. Continue the interrupted work from where the last attempt stopped; do not restart completed steps.`, display: true }, { deliverAs: 'steer', triggerTurn: true });
        }, backoffMs(policy, 1));
        return;
      }
      failure.fallbackIndex += 1;
      recover(ctx); // try the next fallback, or surface
      return;
    }
    report({ state: decision.reason === 'exhausted' ? 'retry_exhausted' : 'error', error: failure.message, http_code: failure.code, attempt: failure.attemptsMade, max_attempts: policy.max_attempts, model: currentModel(ctx), ...usageBody() });
    clearFailure();
  }

  pi.on('session_start', () => { stopped = false; seen.clear(); clearInterval(timer); timer = setInterval(() => refresh().catch(() => {}), 1000); refresh().catch(() => {}); });
  pi.on('session_shutdown', () => { stopped = true; clearInterval(timer); clearFailure(); });
  pi.on('message_end', async (event, ctx) => {
    const message = event.message;
    if (!message || message.role !== 'assistant') return;
    const u = message.usage;
    const label = currentModel(ctx);
    if (label && u && (u.input || u.output || u.cacheRead || u.cacheWrite)) {
      // Counters stay attributed to the model that consumed them, so a
      // fallback or operator switch never relabels earlier usage.
      const prior = usageByModel[label] || { input: 0, cacheRead: 0, cacheWrite: 0, output: 0 };
      usageByModel[label] = {
        input: prior.input + (u.input || 0),
        cacheRead: prior.cacheRead + (u.cacheRead || 0),
        cacheWrite: prior.cacheWrite + (u.cacheWrite || 0),
        output: prior.output + (u.output || 0),
      };
    }
    if (message.stopReason === 'error') {
      failure = {
        code: parseHTTPCode(message.errorMessage),
        message: String(message.errorMessage || 'model call failed').slice(0, 2000),
        attemptsMade: failure ? failure.attemptsMade + 1 : 1,
        retryAfterMs: parseRetryAfterMs(message.errorMessage),
        fallbackIndex: failure ? failure.fallbackIndex : 0,
      };
      report({ state: 'error', error: failure.message, http_code: failure.code, model: currentModel(ctx), ...usageBody() });
      return;
    }
    if (failure || Object.keys(usageByModel).length) {
      clearFailure();
      report({ state: 'healthy', model: currentModel(ctx), ...usageBody() });
    }
  });
  pi.on('agent_settled', async (event, ctx) => {
    if (event.aborted) { clearFailure(); return; } // deliberate operator aborts are not retried
    if (!failure || stopped) return;
    recover(ctx);
  });
  async function refresh() {
    if (busy || stopped) return; busy = true;
    try {
      notices = await request(`/api/sessions/${session}/inbox`);
      for (const n of notices) {
        const target = n.targets.find(t => t.session_id === session);
        const key = n.id + (n.released_at ? ':release' : '');
        if (seen.has(key) || (!n.released_at && target?.acknowledged_at)) continue;
        const text = `Mitos attention ${n.id} from ${n.author}:\n${n.body}\n${n.released_at ? 'RELEASED: recheck other breakpoints, then continue.' : n.pause ? 'BREAKPOINT: stop starting new work and acknowledge when safe. Wait for release.' : 'Read and acknowledge.'}\nUse "$MITOS_BIN" desk acknowledge ${n.id}. Respect the saved project rules.`;
        pi.sendMessage({ customType: 'mitos-attention', content: text, display: true }, { deliverAs: 'steer', triggerTurn: true });
        seen.add(key);
        await request(`/api/sessions/${session}/inbox/${n.id}/delivered`, { release: !!n.released_at });
      }
    } finally { busy = false; }
  }
  pi.on('tool_call', async (event) => {
    // Check the durable inbox afresh at the boundary, independent of push timing.
    try { notices = await request(`/api/sessions/${session}/inbox`); } catch { return { block: true, reason: 'Mitos attention is unavailable. Wait for the local server before starting another tool.' }; }
    const held = notices.filter(n => n.pause && !n.released_at);
    if (!held.length) return;
    const command = event.input?.command || '';
    const control = /^(?:"\$MITOS_BIN"|mitos) desk (?:inbox(?: --wait)?|acknowledge [A-Za-z0-9_-]+|release-notice [A-Za-z0-9_-]+)$/.test(command);
    if (event.toolName === 'bash' && control) return;
    return { block: true, reason: `Mitos breakpoint ${held.map(n => n.id).join(', ')}: ${held.map(n => n.body).join('\n')}. Acknowledge with "$MITOS_BIN" desk acknowledge ID, then wait for release. Already-running work is not cancelled.` };
  });
}
