// The Backup tab's history rows, from the service's run registry.
//
// The table read job_history.json until V4-CONSOLE C2 (a second history the
// service wrote beside the registry, with different content). It now reads
// GetRecentRuns, the same source as the status panel, and this turns a run
// into the row the table has always drawn, so the table itself is unchanged;
// its redesign is GUI audit S11.
//
// A run still going is a row too ('running', drawn as in progress): the
// registry includes live runs, and the old file never showed one at all.

// HISTORY_DAYS is how far back the table looks: the registry's retention.
// The table shows the newest six, as before.
export const HISTORY_DAYS = 30

// MANUAL_NAME is the name the service gave a run with no job, as before.
const MANUAL_NAME = 'Manual backup'

export function runToHistoryRow(run) {
  const done = run.state === 'done'
  return {
    id: run.run_id,
    name: run.job_name || MANUAL_NAME,
    // When it ended, as the old file recorded; when it began, while it runs.
    timestamp: (done && run.ended_at) || run.started_at,
    status: !done ? 'running' : run.success ? 'success' : 'failed',
    message: run.success ? (run.message || '') : (run.error || run.message || ''),
    backupDirs: run.backup_dirs || [],
    backupId: run.backup_id || '',
    useVSS: !!run.use_vss,
  }
}

// runsToHistory maps a GetRecentRuns answer (newest first) to table rows.
export function runsToHistory(resp) {
  return ((resp && resp.runs) || []).map(runToHistoryRow)
}
