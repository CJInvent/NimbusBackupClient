import { useTranslation } from '../../i18n/i18nContext'
import RestoreBetaBox from './RestoreBetaBox'
import RestoreSource from './RestoreSource'
import SearchPanel from './SearchPanel'
import SnapshotGrid from './SnapshotGrid'
import SnapshotMetaBanner from './SnapshotMetaBanner'
import SnapshotContents from './SnapshotContents'
import RestoreForm from './RestoreForm'
import StatusNotice from '../frame/StatusNotice'

// The Restore tab. r is the restore model (hooks/useRestore).
export default function RestoreScreen({ active, r, pbsServers, defaultPBSID, hostname, systemInfo, status }) {
  const { t } = useTranslation()
  return (
    <div className={`tab-content ${active ? 'active' : ''}`}>
      <h2>{t('restoreTitle')}</h2>

      <RestoreBetaBox />

      <RestoreSource r={r} pbsServers={pbsServers} defaultPBSID={defaultPBSID} hostname={hostname} />

      {r.showSearch && <SearchPanel r={r} hostname={hostname} />}

      {r.showSnapshots && <SnapshotGrid r={r} />}

      {r.selectedSnapshot && r.snapshotMeta && <SnapshotMetaBanner snapshotMeta={r.snapshotMeta} />}

      {r.selectedSnapshot && <SnapshotContents r={r} />}

      {r.selectedSnapshot && <RestoreForm r={r} hostname={hostname} systemInfo={systemInfo} />}

      <div className="info-box" style={{marginTop: '20px'}}>
        💡 <strong>{t('restoreInfo')}</strong> {t('restoreInfoText')}<br/>
        {t('restoreInfoText2')}
      </div>

      <StatusNotice status={status} show={active} />
    </div>
  )
}
