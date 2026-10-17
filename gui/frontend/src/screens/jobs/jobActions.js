import { DeleteScheduledJob } from '../../lib/bindings'

// Edit and delete a local scheduled job: f (useBackupForm), jobs
// (useScheduler), app (useAppData), setActiveTab.
export default function makeJobActions({ f, jobs, app, setActiveTab }, showStatus, t) {
  const handleEditJob = (job) => {
    // Load job data into form for editing
    f.setEditingJobId(job.id)
    f.setBackupMode('scheduled')
    f.setScheduleTime(job.scheduleTime)
    f.setRunAtStartup(job.runAtStartup)
    f.setBackupDirs(job.backupDirs.join('\n'))
    f.setSelectedDrives(job.diskTargets || [])
    app.setConfig({...app.config, 'backup-id': job.backupId, usevss: job.useVSS})
    f.setBackupType(job.backupType)
    f.setExcludeList(job.excludeList.join('\n'))
    // Switch to backup tab to show the form
    setActiveTab('backup')
    showStatus(`✏️ ${t('editModeInfo')}`, 'info')
    window.scrollTo({top: 0, behavior: 'smooth'})
  }

  const handleDeleteJob = async (job) => {
    try {
      await DeleteScheduledJob(job.id)
      jobs.setScheduledJobs(jobs.scheduledJobs.filter(j => j.id !== job.id))
      showStatus(t('statusJobDeleted'), 'success')
      // Cancel edit mode if deleting the job being edited
      if (f.editingJobId === job.id) {
        f.setEditingJobId(null)
      }
    } catch (err) {
      showStatus(`❌ ${t('errPrefix')}: ${err}`, 'error')
    }
  }

  return { handleEditJob, handleDeleteJob }
}
