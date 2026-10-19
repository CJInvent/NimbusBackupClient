import { useTranslation } from '../../i18n/i18nContext'

// Back up now, or save a schedule.
export default function BackupModeField({ backupMode, setBackupMode }) {
  const { t } = useTranslation()
  return (
    <div className="form-group">
      <label>{t('executionMode')}</label>
      <div className="nc-seg" style={{marginTop: '10px'}}>
        <button
          type="button"
          className={backupMode === 'oneshot' ? 'active' : ''}
          onClick={() => setBackupMode('oneshot')}
        >
          <span className="compact-text-long">⚡ {t('oneshotMode')}</span>
          <span className="compact-text-short">⚡ {t('oneshotModeShort')}</span>
        </button>
        <button
          type="button"
          className={backupMode === 'scheduled' ? 'active' : ''}
          onClick={() => setBackupMode('scheduled')}
        >
          <span className="compact-text-long">📅 {t('scheduledMode')}</span>
          <span className="compact-text-short">📅 {t('scheduledModeShort')}</span>
        </button>
      </div>
    </div>
  )
}
