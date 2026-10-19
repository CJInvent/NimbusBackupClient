import { useTranslation } from '../../i18n/i18nContext'
import { localizeMessage } from '../../lib/messages'

// The six most recent runs, with Run again on a failed one.
export default function JobHistory({ jobHistory, onRerun }) {
  const { t } = useTranslation()
  return (
    <div className="card" style={{marginTop: '30px'}}>
      <h3 style={{marginTop: 0}}>📜 {t('backupHistory')}</h3>
      <div style={{maxHeight: '400px', overflowY: 'auto'}}>
        {jobHistory.slice(0, 6).map(job => (
          <div key={job.id} style={{
            padding: '15px',
            marginBottom: '10px',
            backgroundColor: job.status === 'success' ? 'color-mix(in srgb, var(--nc-ok) 12%, var(--nc-panel))' : job.status === 'failed' ? 'var(--nc-err-bg)' : 'var(--nc-warn-bg)',
            borderRadius: 'var(--nc-radius)',
            border: `1px solid ${job.status === 'success' ? 'color-mix(in srgb, var(--nc-ok) 45%, var(--nc-border))' : job.status === 'failed' ? 'color-mix(in srgb, var(--nc-err) 45%, var(--nc-border))' : 'var(--nc-warn-border)'}`
          }}>
            <div style={{display: 'flex', justifyContent: 'space-between', alignItems: 'center'}}>
              <div style={{flex: 1}}>
                <div style={{display: 'flex', alignItems: 'center', gap: '10px'}}>
                  <span style={{fontSize: '20px'}}>
                    {job.status === 'success' ? '✅' : job.status === 'failed' ? '❌' : '⏳'}
                  </span>
                  <strong>{job.name}</strong>
                </div>
                <div style={{fontSize: '13px', color: 'var(--nc-text-dim)', marginTop: '5px', marginLeft: '30px'}}>
                  🕐 {new Date(job.timestamp).toLocaleString('fr-FR')}
                </div>
                {job.message && (
                  <div style={{fontSize: '13px', color: 'var(--nc-text-dim)', marginTop: '5px', marginLeft: '30px'}}>
                    💬 {localizeMessage(t, job.message)}
                  </div>
                )}
              </div>
              {job.status === 'failed' && (
                <button
                  className="btn"
                  style={{padding: '8px 15px', fontSize: '14px'}}
                  onClick={() => onRerun(job)}
                >
                  🔄 {t('rerun')}
                </button>
              )}
            </div>
          </div>
        ))}
      </div>
    </div>
  )
}
