// The Backup tab's history rows from the service's runs (src/lib/history.js,
// docs/V4-CONSOLE.md phase C2). The table component is unchanged and has its
// own render snapshot; this pins the mapping that feeds it, so a run reads the
// way a job_history.json entry did.
import { test } from 'node:test'
import assert from 'node:assert/strict'
import { runToHistoryRow, runsToHistory, HISTORY_DAYS } from '../src/lib/history.js'

const failed = {
  run_id: 'run-1792300000-4', job_id: 'job-7', job_name: 'Nightly', trigger: 'schedule',
  backup_id: 'WS-01', backup_type: 'directory', state: 'done',
  started_at: '2026-10-17T07:00:00Z', ended_at: '2026-10-17T07:04:00Z',
  percent: 40, message: 'uploading', success: false, error: 'PBS HTTP 403 for datastore status',
  backup_dirs: ['C:\\Users', 'D:\\Data'], use_vss: true,
}

test('a failed run reads as the failed row the table drew', () => {
  assert.deepEqual(runToHistoryRow(failed), {
    id: 'run-1792300000-4',
    name: 'Nightly',
    timestamp: '2026-10-17T07:04:00Z',
    status: 'failed',
    message: 'PBS HTTP 403 for datastore status',
    backupDirs: ['C:\\Users', 'D:\\Data'],
    backupId: 'WS-01',
    useVSS: true,
  })
})

test('a successful run carries its message; a run with no job is a manual backup', () => {
  const row = runToHistoryRow({ ...failed, job_name: '', success: true, error: '', message: 'Backup completed successfully' })
  assert.equal(row.status, 'success')
  assert.equal(row.message, 'Backup completed successfully')
  assert.equal(row.name, 'Manual backup')
})

test('a run still going is in progress, timed from its start', () => {
  const row = runToHistoryRow({ ...failed, state: 'running', ended_at: undefined, success: false, error: '' })
  assert.equal(row.status, 'running')
  assert.equal(row.timestamp, '2026-10-17T07:00:00Z')
})

test('Run again has what it loads, even for a run that recorded none', () => {
  // historyActions.handleRerun joins backupDirs; it must be a list.
  const row = runToHistoryRow({ run_id: 'run-1-1', state: 'done', success: false, started_at: '2026-10-17T07:00:00Z' })
  assert.deepEqual(row.backupDirs, [])
  assert.equal(row.useVSS, false)
  assert.equal(row.backupId, '')
})

test('the answer is mapped in the order the service gave it (newest first)', () => {
  const rows = runsToHistory({ runs: [{ ...failed, run_id: 'b' }, { ...failed, run_id: 'a' }] })
  assert.deepEqual(rows.map(r => r.id), ['b', 'a'])
  assert.deepEqual(runsToHistory(null), [])
  assert.deepEqual(runsToHistory({ runs: null }), [])
})

test('the history looks back as far as the service keeps runs', () => {
  // gui/api runs.go: defaultRetain is 30 days, and /runs/recent refuses more.
  assert.equal(HISTORY_DAYS, 30)
})
