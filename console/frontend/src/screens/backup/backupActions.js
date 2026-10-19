import { StartBackup, StopBackup, SaveScheduledJob, UpdateScheduledJob } from '../../lib/bindings'

// The backup form's handlers, made per render over the current state:
// f (useBackupForm), run (useBackupRun), jobs (useScheduler), app
// (useAppData).
export default function makeBackupActions({ f, run, jobs, app }, showStatus, t) {
  const handleStartBackup = async () => {
    if (!StartBackup) {
      showStatus('❌ ' + t('errNoRuntime'), 'error')
      return
    }

    // Parse backup directories (one per line)
    const dirList = f.backupDirs.split('\n').map(d => d.trim()).filter(d => d)

    if (f.backupType === 'directory' && dirList.length === 0) {
      showStatus('❌ ' + t('errNeedDirectory'), 'error')
      return
    }

    if (f.backupType === 'machine' && f.selectedDrives.length === 0) {
      showStatus('❌ ' + t('errNeedDisk'), 'error')
      return
    }

    // Scheduled mode - save or update job instead of executing immediately
    if (f.backupMode === 'scheduled') {
      if (!SaveScheduledJob || !UpdateScheduledJob) {
        showStatus('❌ ' + t('errNoScheduler'), 'error')
        return
      }

      const jobData = {
        id: f.editingJobId || Date.now().toString(),
        name: `Backup ${app.config['backup-id'] || app.hostname}`,
        scheduleTime: f.scheduleTime,
        runAtStartup: f.runAtStartup,
        backupDirs: dirList,
        diskTargets: f.selectedDrives,
        backupId: app.config['backup-id'],
        useVSS: app.config.usevss,
        backupType: f.backupType,
        excludeList: f.excludesForType()
      }

      // Save or update to backend
      try {
        if (f.editingJobId) {
          // Update existing job
          await UpdateScheduledJob(jobData)
          jobs.setScheduledJobs(jobs.scheduledJobs.map(j => j.id === f.editingJobId ? jobData : j))
          showStatus(`✅ ${t('scheduleUpdated').replace('{time}', f.scheduleTime)}`, 'success')
          f.setEditingJobId(null)
        } else {
          // Create new job
          await SaveScheduledJob(jobData)
          jobs.setScheduledJobs([...jobs.scheduledJobs, jobData])
          showStatus(`✅ ${t('scheduleCreated').replace('{time}', f.scheduleTime)}`, 'success')
        }
        // Reset form after save
        f.setScheduleTime('02:00')
        f.setRunAtStartup(false)
        f.setBackupDirs('')
      } catch (err) {
        showStatus(`❌ ${t('errPrefix')}: ${err}`, 'error')
      }
      return
    }

    // One-shot mode - execute immediately
    run.setProgress(5)

    try {
      await StartBackup(
        f.backupType,
        dirList,
        f.selectedDrives,
        f.excludesForType(),
        app.config['backup-id'],
        app.config.usevss,
        ''
      )
      // Backup started in background - progress will be shown via events
    } catch (err) {
      run.setProgress(0)
      showStatus(`❌ ${err}`, 'error')
    }
  }

  const handleStopBackup = async () => {
    if (!StopBackup) {
      showStatus('❌ ' + t('errNoRuntime'), 'error')
      return
    }
    // Ask the backend to stop; it winds the engine down cleanly (releases the
    // VSS snapshot, discards the partial index). The tile clears itself when the
    // backup:complete event arrives, so we only surface a one-off "stopping" note.
    try {
      await StopBackup()
      showStatus(`🛑 ${t('statusBackupStopping')}`, 'info')
    } catch (err) {
      showStatus(`❌ ${err}`, 'error')
    }
  }

  // Leave a scheduled job's edit without saving.
  const handleCancelJobEdit = () => {
    f.setEditingJobId(null)
    f.setScheduleTime('02:00')
    f.setRunAtStartup(false)
    f.setBackupDirs('')
    f.setExcludeList('')
    f.setBackupType('directory')
    showStatus(`✖️ ${t('statusEditCancelled')}`, 'info')
  }

  return { handleStartBackup, handleStopBackup, handleCancelJobEdit }
}
