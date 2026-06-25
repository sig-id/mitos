export function formatRelative(ts) {
  if (!ts) return ''
  const diff = Date.now() - ts
  const abs = Math.abs(diff)
  const past = diff >= 0
  const units = [
    ['y', 31536000],
    ['d', 86400],
    ['h', 3600],
    ['m', 60],
    ['s', 1],
  ]
  for (const [label, sec] of units) {
    if (abs >= sec) {
      const v = Math.floor(abs / sec)
      return past ? `${v}${label} ago` : `in ${v}${label}`
    }
  }
  return 'just now'
}

export function formatTime(ts) {
  if (!ts) return ''
  const d = new Date(ts)
  return d.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })
}

export function formatCron(c) {
  // Very simplified human-readable cron
  const map = {
    '0 9 * * *': 'Every day at 9:00',
    '0 */6 * * *': 'Every 6 hours',
    '0 * * * *': 'Every hour',
    '*/15 * * * *': 'Every 15 minutes',
  }
  return map[c] || (c ? `cron: ${c}` : '')
}

export function describeMode(m) {
  return ({
    loop: 'Continuous',
    periodic: 'Scheduled',
    oneshot: 'One-off',
  })[m] || m
}
