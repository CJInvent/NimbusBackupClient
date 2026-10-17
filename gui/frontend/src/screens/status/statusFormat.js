// The status page's formatting and its per-day rollup.

export function fmtBytes(n) {
  if (!n) return '0 B'
  const u = ['B', 'KB', 'MB', 'GB', 'TB']
  let i = 0
  let v = n
  while (v >= 1024 && i < u.length - 1) { v /= 1024; i++ }
  return `${v.toFixed(v < 10 && i > 0 ? 1 : 0)} ${u[i]}`
}

export function fmtDuration(sec) {
  if (sec == null || sec < 0) return '—'
  const s = Math.floor(sec)
  if (s < 60) return `${s}s`
  if (s < 3600) return `${Math.floor(s / 60)}m ${s % 60}s`
  return `${Math.floor(s / 3600)}h ${Math.floor((s % 3600) / 60)}m`
}

export function fmtWhen(iso) {
  if (!iso) return '—'
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return '—'
  return d.toLocaleString()
}

/*
 * Tri-state, not boolean. `reachable` is true, false, or null — and null means
 * "nobody has checked", which is a different fact from "it answered no". A
 * tile that says OFFLINE for an unprobed destination sends a technician to
 * inspect a firewall for what is actually a config problem. The service
 * preserves this distinction all the way here (gui/api/connections.go); the
 * panel must not flatten it at the last step.
 */
export function reachabilityState(reachable) {
  if (reachable === true) return 'ok'
  if (reachable === false) return 'down'
  return 'unknown'
}

/*
 * Per-day rollup showing the WORST outcome of each day.
 *
 * Worst rather than latest: a day with eleven successes and one failure is a
 * day something went wrong, and a panel that shows it green because the last
 * run happened to pass is worse than no panel at all.
 */
export function rollupByDay(runs) {
  const days = new Map()
  for (const r of runs) {
    const d = new Date(r.started_at)
    if (Number.isNaN(d.getTime())) continue
    const key = d.toISOString().slice(0, 10)
    const cur = days.get(key) || { key, total: 0, failed: 0, running: 0, last: null }
    cur.total++
    if (r.state !== 'done') cur.running++
    else if (!r.success) cur.failed++
    if (!cur.last || r.started_at.localeCompare(cur.last) === 1) cur.last = r.started_at
    days.set(key, cur)
  }
  // localeCompare rather than a < comparison: this used to sit in a .jsx
  // file, which the i18n auditor parses for JSX, and a bare < inside an
  // arrow body reads to it as a tag opening.
  return Array.from(days.values()).sort((a, b) => b.key.localeCompare(a.key))
}
