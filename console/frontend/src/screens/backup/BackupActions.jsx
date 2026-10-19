import { useTranslation } from '../../i18n/i18nContext'

// Start (or save the schedule), Stop, and leave a job's edit.
export default function BackupActions({ backupMode, progress, editingJobId, onStart, onStop, onCancelEdit }) {
  const { t } = useTranslation()
  return (
    <>
      {!(backupMode === 'oneshot' && progress > 0 && progress < 100) && (
        <button className="btn" onClick={onStart}>
          {backupMode === 'oneshot'
            ? `🚀 ${t('startBackup')}`
            : (editingJobId ? `✏️ ${t('updateSchedule')}` : `💾 ${t('saveSchedule')}`)
          }
        </button>
      )}
      {backupMode === 'oneshot' && (
        <button className="btn btn-secondary" onClick={onStop} disabled={progress === 0}>{t('stopBackup')}</button>
      )}
      {backupMode === 'scheduled' && editingJobId && (
        <button className="btn btn-secondary" onClick={onCancelEdit}>
          ✖️ {t('cancel')}
        </button>
      )}
    </>
  )
}
