import { useTranslation } from '../../i18n/i18nContext'

// The local scheduled jobs, each with Edit and Delete.
export default function ScheduledJobs({ scheduledJobs, onEdit, onDelete }) {
  const { t } = useTranslation()
  return (
    <div className="card" style={{marginTop: '30px'}}>
      <h3 style={{marginTop: 0}}>📅 {t('scheduledJobs')}</h3>
      {scheduledJobs.map(job => (
        <div key={job.id} style={{
          padding: '15px',
          marginBottom: '10px',
          backgroundColor: 'var(--nc-panel-head)',
          borderRadius: '8px',
          border: '1px solid var(--nc-border)'
        }}>
          <div style={{display: 'flex', justifyContent: 'space-between', alignItems: 'center'}}>
            <div>
              <strong>{job.name}</strong>
              <div style={{fontSize: '14px', color: 'var(--nc-text-dim)', marginTop: '5px'}}>
                ⏰ {job.scheduleTime} {job.runAtStartup && '• 🚀 Au démarrage'}
              </div>
              <div style={{fontSize: '13px', color: 'var(--nc-text-dim)', marginTop: '3px'}}>
                📁 {job.backupDirs.join(', ')}
              </div>
            </div>
            <div style={{display: 'flex', gap: '10px'}}>
              <button
                className="btn"
                style={{padding: '8px 15px', fontSize: '14px'}}
                onClick={() => onEdit(job)}
              >
                ✏️ {t('editJob')}
              </button>
              <button
                className="btn btn-secondary"
                style={{padding: '8px 15px', fontSize: '14px'}}
                onClick={() => onDelete(job)}
              >
                🗑️ {t('deleteJob')}
              </button>
            </div>
          </div>
        </div>
      ))}
    </div>
  )
}
