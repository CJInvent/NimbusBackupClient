import { useTranslation } from '../../i18n/i18nContext'

// The daily time and run-at-startup of a local scheduled job.
export default function ScheduleOptions({ editingJobId, scheduleTime, setScheduleTime, runAtStartup, setRunAtStartup }) {
  const { t } = useTranslation()
  return (
    <div className="card" style={{marginTop: '20px', padding: '20px'}}>
      <h3 style={{marginTop: 0}}>⏰ {t('schedulingConfig')}</h3>

      {editingJobId && (
        <div className="info-box" style={{backgroundColor: 'var(--nc-warn-bg)', borderColor: 'var(--nc-warn-border)', marginBottom: '15px'}}>
          ✏️ <strong>{t('editMode')}</strong> - {t('editModeText')}
        </div>
      )}

      <div className="form-group">
        <label>{t('dailyExecutionTime')}</label>
        <input
          type="time"
          value={scheduleTime}
          onChange={(e) => setScheduleTime(e.target.value)}
          style={{width: '200px', padding: '10px', fontSize: '16px'}}
        />
      </div>

      <div className="form-group">
        <label style={{display: 'flex', alignItems: 'center', gap: '10px', cursor: 'pointer'}}>
          <input
            type="checkbox"
            checked={runAtStartup}
            onChange={(e) => setRunAtStartup(e.target.checked)}
            style={{width: '20px', height: '20px', cursor: 'pointer'}}
          />
          <span>🚀 {t('runAtStartup')}</span>
        </label>
      </div>

      <div className="info-box" style={{backgroundColor: 'color-mix(in srgb, var(--nc-accent) 8%, var(--nc-panel-head))'}}>
        💡 {t('schedulingInfo')} <strong>{scheduleTime}</strong>
        {runAtStartup && <><br/>{t('andAtStartup')}</>}
      </div>
    </div>
  )
}
