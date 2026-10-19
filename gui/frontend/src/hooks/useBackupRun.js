import { useState, useRef } from 'react'
import { GetActiveRuns, GetRecentRuns } from '../lib/bindings'
import { HISTORY_DAYS, runsToHistory } from '../lib/history'
import usePolling from './usePolling'

const idleStats = () => ({
  startTime: null,
  lastUpdate: null,
  lastPercent: 0,
  speed: 0,
  eta: null,
  // Structured live stats (from the backup:stats event)
  bytesDone: 0,
  bytesTotal: 0,
  newChunks: 0,
  reusedChunks: 0,
  failedChunks: 0,
  currentDir: ''
})

// The run in flight: its percentage, phase and live statistics, from the
// service's run registry.
//
// Poll the service for runs in flight — the observation path that works for
// every trigger.
//
// Wails events only ever describe a backup THIS process started, because
// they are emitted in the GUI's own process (docs/V4-PIPELINE.md §2.4). A
// scheduled backup, or any backup already under way when the window is
// opened, produces no events here at all. Polling /runs/active is what makes
// those visible, and it drives the SAME state the events do, so the card
// renders identically however the run began — which is the requirement.
//
// Three seconds: fast enough that the Start button still feels immediate
// once events are gone, slow enough to be nothing on a machine that is busy
// reading a disk.
export default function useBackupRun(showStatus, setJobHistory) {
  const [progress, setProgress] = useState(0)
  const [backupPhase, setBackupPhase] = useState('')   // current engine phase, shown once in the card
  const [backupStats, setBackupStats] = useState(idleStats())
  const watched = useRef(null)

  usePolling(async (alive) => {
    try {
      const resp = await GetActiveRuns()
      if (!alive()) return

      const runs = resp && resp.runs
      const run = (runs && runs.length) ? runs[0] : null

      if (!run) {
        // A run we were watching has finished. Nothing pushes that fact to
        // us any more -- the service emits no Wails events, because the
        // process that runs backups has no window to emit into -- so the
        // outcome is resolved the same way the progress was: by asking.
        if (watched.current) {
          const finishedId = watched.current
          watched.current = null
          try {
            const recent = await GetRecentRuns(1)
            const done = (recent && recent.runs || []).find(r => r.run_id === finishedId)
            if (done && alive()) {
              setBackupPhase('')
              setProgress(done.success ? 100 : 0)
              setBackupStats({ startTime: null, lastUpdate: null, lastPercent: 0, speed: 0, eta: null, bytesDone: 0, bytesTotal: 0, newChunks: 0, reusedChunks: 0, failedChunks: 0, currentDir: '' })
              showStatus(done.success ? '✅ ' + (done.message || 'Backup complete') : '❌ ' + (done.error || done.message || 'Backup failed'), done.success ? 'success' : 'error')
              // Job history is NOT appended here. The service's run
              // registry is the history (lib/history.js), and the history
              // poll reads it -- appending locally as well double-counted
              // every backup the GUI happened to be open for.
              // Refresh the history immediately rather than waiting for
              // its own slower poll, so a finished backup appears in the
              // list at the moment the card clears.
              try {
                const h = await GetRecentRuns(HISTORY_DAYS)
                if (alive()) setJobHistory(runsToHistory(h))
              } catch (err) { /* history poll will catch up */ }
            }
          } catch (err) { /* the panel already shows the connection state */ }
        }
        return
      }
      watched.current = run.run_id

      // A run in "preparing" reports 0%, and 0 is also how the card decides
      // it has nothing to show. Floor it at 1 so a backup that has been
      // decided but has not yet read a byte still appears — on a large
      // volume that gap is minutes, and showing nothing through it is the
      // bug this poll exists to fix.
      setProgress(Math.max(1, Math.round(run.percent || 0)))
      if (run.message) setBackupPhase(run.message)

      // Elapsed comes from the SERVER's clock and the run's real start, not
      // from when this window happened to open. Anchoring to first-sight
      // (what the event path does) makes the rate and ETA of a run already
      // in flight badly wrong, and opening the GUI mid-backup is exactly
      // the case being fixed here.
      const serverMs = Date.parse(resp.server_time)
      const serverNow = Number.isNaN(serverMs) ? Date.now() : serverMs
      const startedMs = Date.parse(run.started_at)
      const startTime = Number.isNaN(startedMs) ? Date.now() : startedMs
      const elapsed = Math.max(0, (serverNow - startTime) / 1000)
      const done = run.bytes_done || 0
      const total = run.bytes_total || 0
      const byteRate = elapsed > 3 && done > 0 ? done / elapsed : 0
      // Padded x1.15 and rendered as ">=", same as the event path: the whole
      // disk must be READ however much of it is reused, so an estimate that
      // errs should err high.
      const eta = byteRate > 0 && total > done
        ? Math.round(((total - done) / byteRate) * 1.15)
        : null

      setBackupStats(prev => ({
        ...prev,
        startTime,
        lastUpdate: Date.now(),
        lastPercent: Math.round(run.percent || 0),
        bytesDone: done,
        bytesTotal: total,
        newChunks: run.new_chunks || 0,
        reusedChunks: run.reused_chunks || 0,
        failedChunks: run.failed_chunks || 0,
        currentDir: run.current_dir || '',
        byteRate,
        eta,
      }))
    } catch (err) {
      // Deliberately quiet. The service restarting would otherwise log
      // every three seconds about a condition the panel already shows.
    }
  }, 3000, !!GetActiveRuns)

  return { progress, setProgress, backupPhase, backupStats }
}
