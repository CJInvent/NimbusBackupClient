import { useTranslation } from '../../i18n/i18nContext'
import { isVolumeSnapshot } from '../../lib/snapshots'

// The snapshots found for the backup ID, one card each.
export default function SnapshotGrid({ r }) {
  const { t } = useTranslation()
  return (
    <div style={{marginTop: '20px'}}>
      <h3>{t('availableSnapshots')}</h3>
      <div className="grid">
        {r.snapshots.length === 0 ? (
          <p style={{color: 'var(--nc-text-dim)'}}>{t('noSnapshotFound')}</p>
        ) : (
          r.snapshots.map((snap, idx) => {
            const isActive = r.selectedSnapshot && r.selectedSnapshot.id === snap.id && r.selectedSnapshot.backup_id === snap.backup_id
            return (
              <div
                key={idx}
                className={'card selectable' + (isActive ? ' nc-selected' : '')}
                onClick={() => r.handleSelectSnapshot(snap)}
              >
                <h3>📸 {snap.time}</h3>
                <p style={{color: 'var(--nc-text-dim)', fontSize: '14px', marginTop: '5px'}}>
                  {snap.backup_id}<br/>
                  {isVolumeSnapshot(snap)
                    ? <span>💿 {t('typeVolume')}</span>
                    : <span>📁 {t('typeFile')}</span>}
                </p>
                <button className="btn" style={{marginTop: '10px', width: '100%'}}>
                  {isActive ? `✓ ${t('snapshotSelected')}` : t('selectSnapshot')}
                </button>
              </div>
            )
          })
        )}
      </div>
    </div>
  )
}
