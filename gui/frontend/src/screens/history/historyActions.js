// Load a failed run's folders and settings back into the backup form.
export default function makeHistoryActions({ f, app }, showStatus, t) {
  const handleRerun = (job) => {
    // Re-run failed job
    f.setBackupDirs(job.backupDirs.join('\n'))
    app.setConfig({...app.config, 'backup-id': job.backupId, usevss: job.useVSS})
    showStatus(t('configLoaded'), 'success')
    window.scrollTo({top: 0, behavior: 'smooth'})
  }
  return { handleRerun }
}
