import { useTranslation } from '../../i18n/i18nContext'

// Which storage server and which backup ID to look in, and the two ways in:
// list its snapshots, or search across them.
export default function RestoreSource({ r, pbsServers, defaultPBSID, hostname }) {
  const { t } = useTranslation()
  return (
    <>
      {/* PBS server selector + Backup ID */}
      <div style={{display: 'flex', gap: '15px', flexWrap: 'wrap'}}>
        <div className="form-group" style={{flex: '1 1 240px'}}>
          <label>{t('restorePBSServer')}</label>
          <select
            value={r.restorePBSID}
            onChange={(e) => {
              r.setRestorePBSID(e.target.value)
              r.setShowSnapshots(false)
              r.setSelectedSnapshot(null)
              r.setSnapshotEntries([])
            }}
          >
            {pbsServers.length === 0 && <option value="">{t('noPBSServer')}</option>}
            {pbsServers.map(s => (
              <option key={s.id} value={s.id}>
                {s.name} {s.id === defaultPBSID ? '⭐' : ''}
              </option>
            ))}
          </select>
        </div>
        <div className="form-group" style={{flex: '2 1 320px'}}>
          <label>{t('backupIDToRestore')}</label>
          <input
            type="text"
            value={r.restoreBackupId || hostname}
            onChange={(e) => r.setRestoreBackupId(e.target.value)}
            placeholder={hostname || "hostname ou ID personnalisé"}
          />
        </div>
      </div>

      <button className="btn" onClick={r.handleListSnapshots}>📋 {t('listSnapshots')}</button>
      <button
        className="btn"
        type="button"
        onClick={() => r.setShowSearch(v => !v)}
        style={{marginLeft: '8px'}}
      >
        🔎 {t('searchTitle')}
      </button>
    </>
  )
}
