import { useState, useEffect } from 'react'
import { useTranslation } from './i18n/i18nContext'

/*
 * StatusPanel — what a locked console shows, and the only thing it shows.
 *
 * Rendered INSTEAD OF the tabbed UI when the org sets `gui_read_only`, not
 * alongside it with the controls disabled. That is deliberate and matches the
 * portal's View::cap rule: a denied control is not in the markup. A greyed-out
 * Start button still tells a user the capability exists and invites them to
 * find out why it is off.
 *
 * This file is PRESENTATION ONLY. The control is the service refusing every
 * mutating local-API call while locked (gui/api/readonly.go) — so a modified
 * front end that renders the old UI anyway gets 403s, and nothing here is
 * load-bearing for security. What it is load-bearing for is the technician
 * standing at the machine being able to answer "is this thing working?"
 * without calling the MSP.
 *
 * Everything is derived from three polled endpoints and nothing else:
 *   /connections   what this machine can reach
 *   /runs/active   what is running right now
 *   /runs/recent   the last seven days
 */

// Poll cadences. The live run moves; connections and history do not, and the
// service probes destinations on its own 60s sweep, so asking faster than that
// only costs round trips.
const ACTIVE_POLL_MS = 3000
const SLOW_POLL_MS = 30000

const RECENT_DAYS = 7
// A machine backing up hourly has ~168 runs in a week. The table shows a
// per-day rollup with the worst outcome of each day, and the most recent runs
// in full — a flat list of 168 rows is not something anyone reads.
const DETAIL_ROWS = 10

function fmtBytes(n) {
  if (!n) return '0 B'
  const u = ['B', 'KB', 'MB', 'GB', 'TB']
  let i = 0
  let v = n
  while (v >= 1024 && i < u.length - 1) { v /= 1024; i++ }
  return `${v.toFixed(v < 10 && i > 0 ? 1 : 0)} ${u[i]}`
}

function fmtDuration(sec) {
  if (sec == null || sec < 0) return '—'
  const s = Math.floor(sec)
  if (s < 60) return `${s}s`
  if (s < 3600) return `${Math.floor(s / 60)}m ${s % 60}s`
  return `${Math.floor(s / 3600)}h ${Math.floor((s % 3600) / 60)}m`
}

function fmtWhen(iso) {
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
function reachabilityState(reachable) {
  if (reachable === true) return 'ok'
  if (reachable === false) return 'down'
  return 'unknown'
}

function ConnectionTile({ title, state, lines }) {
  const { t } = useTranslation()
  const label = state === 'ok' ? t('panelOnline')
    : state === 'down' ? t('panelOffline')
      : t('panelUnknown')
  const icon = state === 'ok' ? '●' : state === 'down' ? '●' : '○'

  return (
    <div className={`panel-tile panel-tile-${state}`}>
      <div className="panel-tile-head">
        <span className="panel-tile-dot" aria-hidden="true">{icon}</span>
        <span className="panel-tile-title">{title}</span>
        <span className="panel-tile-state">{label}</span>
      </div>
      {lines.filter(Boolean).map((l, i) => (
        <div className="panel-tile-line" key={i}>{l}</div>
      ))}
    </div>
  )
}

function LiveRun({ run, serverTime }) {
  const { t } = useTranslation()
  if (!run) return <div className="panel-idle">{t('panelNoRunNow')}</div>

  const started = Date.parse(run.started_at)
  const now = Date.parse(serverTime) || Date.now()
  const elapsed = Number.isNaN(started) ? null : (now - started) / 1000
  const pct = Math.max(0, Math.min(100, Math.round(run.percent || 0)))

  return (
    <div className="panel-live">
      <div className="panel-live-head">
        <span className="panel-live-name">{run.job_name || run.backup_id || t('panelBackup')}</span>
        <span className="panel-live-pct">{pct}%</span>
      </div>
      <div className="panel-bar"><div className="panel-bar-fill" style={{ width: `${pct}%` }} /></div>
      <div className="panel-live-msg">{run.message || ''}</div>
      <div className="panel-live-grid">
        <div><span>{t('panelTrigger')}</span> {run.trigger || '—'}</div>
        <div><span>{t('panelElapsed')}</span> {fmtDuration(elapsed)}</div>
        <div><span>{t('panelData')}</span> {fmtBytes(run.bytes_done)}{run.bytes_total ? ` / ${fmtBytes(run.bytes_total)}` : ''}</div>
        <div><span>{t('panelChunks')}</span> {(run.new_chunks || 0)} / {(run.reused_chunks || 0)}</div>
      </div>
      {run.current_dir ? <div className="panel-live-dir">{run.current_dir}</div> : null}
    </div>
  )
}

/*
 * Per-day rollup showing the WORST outcome of each day.
 *
 * Worst rather than latest: a day with eleven successes and one failure is a
 * day something went wrong, and a panel that shows it green because the last
 * run happened to pass is worse than no panel at all.
 */
function rollupByDay(runs) {
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
  // localeCompare rather than a < comparison: the i18n auditor parses this
  // file for JSX, and a bare < inside an arrow body reads to it as a tag
  // opening. Avoiding the ambiguity here is cheaper than teaching the
  // auditor JavaScript, and it does not weaken what it catches.
  return Array.from(days.values()).sort((a, b) => b.key.localeCompare(a.key))
}

export default function StatusPanel({ version, hostname }) {
  const { t } = useTranslation()
  const [conns, setConns] = useState(null)
  const [active, setActive] = useState(null)
  const [recent, setRecent] = useState([])
  const [serviceUp, setServiceUp] = useState(true)

  const w = typeof window !== 'undefined' && window.go && window.go.main ? window.go.main.App : null

  // Fast loop: what is running.
  useEffect(() => {
    if (!w || !w.GetActiveRuns) return
    let cancelled = false
    const poll = async () => {
      try {
        const resp = await w.GetActiveRuns()
        if (cancelled) return
        setServiceUp(true)
        const runs = (resp && resp.runs) || []
        setActive(runs.length ? runs[0] : null)
      } catch {
        if (!cancelled) setServiceUp(false)
      }
    }
    poll()
    const id = setInterval(poll, ACTIVE_POLL_MS)
    return () => { cancelled = true; clearInterval(id) }
  }, [w])

  // Slow loop: connections and history. The service probes destinations on its
  // own 60s sweep, so polling this faster only costs round trips.
  useEffect(() => {
    if (!w) return
    let cancelled = false
    const poll = async () => {
      try {
        if (w.GetConnections) {
          const c = await w.GetConnections()
          if (!cancelled) setConns(c)
        }
        if (w.GetRecentRuns) {
          const r = await w.GetRecentRuns(RECENT_DAYS)
          if (!cancelled) setRecent((r && r.runs) || [])
        }
      } catch {
        /* the service tile already says the service is not answering */
      }
    }
    poll()
    const id = setInterval(poll, SLOW_POLL_MS)
    return () => { cancelled = true; clearInterval(id) }
  }, [w])

  const pbs = (conns && conns.pbs) || []
  const cp = (conns && conns.control_plane) || {}
  const days = rollupByDay(recent)
  const detail = recent.slice(0, DETAIL_ROWS)

  return (
    <div className="panel">
      <header className="panel-head">
        <div>
          <h1 className="panel-title">{t('panelTitle')}</h1>
          <div className="panel-sub">{hostname || ''}{version ? ` · ${version}` : ''}</div>
        </div>
        <div className="panel-managed">{t('panelManagedNotice')}</div>
      </header>

      <section className="panel-section">
        <h2 className="panel-h2">{t('panelConnections')}</h2>
        <div className="panel-tiles">
          <ConnectionTile
            title={t('panelService')}
            state={serviceUp ? 'ok' : 'down'}
            lines={[serviceUp ? t('panelServiceRunning') : t('panelServiceDown')]}
          />
          <ConnectionTile
            title={t('panelControlServer')}
            state={!cp.configured ? 'unknown' : cp.connected ? 'ok' : 'down'}
            lines={[
              cp.configured ? cp.server_host : t('panelNotManaged'),
              cp.configured && cp.last_success ? `${t('panelLastCheckin')} ${fmtWhen(cp.last_success)}` : null,
              cp.configured && cp.last_error ? cp.last_error : null,
            ]}
          />
          {pbs.map(p => (
            <ConnectionTile
              key={p.id}
              title={p.name || p.id}
              state={reachabilityState(p.reachable)}
              lines={[
                p.host,
                p.datastore,
                p.checked_at ? `${t('panelChecked')} ${fmtWhen(p.checked_at)}` : t('panelNeverChecked'),
              ]}
            />
          ))}
          {pbs.length === 0 ? <div className="panel-empty">{t('panelNoDestinations')}</div> : null}
        </div>
      </section>

      <section className="panel-section">
        <h2 className="panel-h2">{t('panelRunningNow')}</h2>
        <LiveRun run={active} serverTime={conns && conns.server_time} />
      </section>

      <section className="panel-section">
        <h2 className="panel-h2">{t('panelSevenDays')}</h2>
        {days.length === 0 ? (
          <div className="panel-empty">{t('panelNoRuns')}</div>
        ) : (
          <div className="panel-days">
            {days.map(d => (
              <div className={`panel-day ${d.running ? 'is-running' : d.failed ? 'is-failed' : 'is-ok'}`} key={d.key}>
                <div className="panel-day-date">{d.key}</div>
                <div className="panel-day-count">
                  {d.failed > 0
                    ? t('panelDayFailed').replace('{failed}', d.failed).replace('{total}', d.total)
                    : t('panelDayOk').replace('{total}', d.total)}
                </div>
              </div>
            ))}
          </div>
        )}

        {detail.length > 0 && (
          <table className="panel-table">
            <thead>
              <tr>
                <th>{t('panelWhen')}</th>
                <th>{t('panelJob')}</th>
                <th>{t('panelTrigger')}</th>
                <th>{t('panelDuration')}</th>
                <th>{t('panelResult')}</th>
              </tr>
            </thead>
            <tbody>
              {detail.map(r => (
                <tr key={r.run_id}>
                  <td>{fmtWhen(r.started_at)}</td>
                  <td>{r.job_name || r.backup_id || '—'}</td>
                  <td>{r.trigger || '—'}</td>
                  <td>{r.state !== 'done'
                    ? t('panelInProgress')
                    : fmtDuration((Date.parse(r.ended_at) - Date.parse(r.started_at)) / 1000)}</td>
                  <td className={r.state !== 'done' ? 'is-running' : r.success ? 'is-ok' : 'is-failed'}>
                    {r.state !== 'done'
                      ? t('panelInProgress')
                      : r.success ? t('panelSucceeded') : (r.error || t('panelFailed'))}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </section>
    </div>
  )
}
