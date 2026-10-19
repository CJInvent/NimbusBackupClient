import { useTranslation } from '../../i18n/i18nContext'
import ControlServerCard from './ControlServerCard'
import ServerWelcome from './ServerWelcome'
import ServerForm from './ServerForm'
import ServerTable from './ServerTable'
import StatusNotice from '../frame/StatusNotice'

// The Servers tab: the control server, then the storage servers. srv is
// usePbsServers with its actions; cp is the control-server card's state.
export default function SetupScreen({ active, srv, cp, config, appVersion, status }) {
  const { t } = useTranslation()
  const form = (first) => (
    <ServerForm
      first={first}
      editingServer={srv.editingServer}
      serverFormData={srv.serverFormData}
      setServerFormData={srv.setServerFormData}
      onAdd={srv.handleAddPBSServer}
      onUpdate={srv.handleUpdatePBSServer}
      onCancel={srv.handleCancelEdit}
    />
  )
  return (
    <div className={`tab-content ${active ? 'active' : ''}`}>
      <ControlServerCard cpStatus={cp.cpStatus} cpForm={cp.cpForm} setCpForm={cp.setCpForm} onSave={cp.saveControlServer} />
      <h2>🖥️ {t('serversTitle')}</h2>

      {/* Show form first if no servers configured */}
      {srv.pbsServers.length === 0 ? (
        <>
          <ServerWelcome config={config} appVersion={appVersion} />
          {form(true)}
        </>
      ) : (
        <>
          {/* Multi-PBS info for users with existing servers */}
          <div className="info-box" style={{marginBottom: '20px'}}>
            💡 <strong>{t('multiPBSInfo')}</strong> {t('multiPBSText')}<br/>
            {t('multiPBSExample')}
          </div>

          <ServerTable
            pbsServers={srv.pbsServers}
            defaultPBSID={srv.defaultPBSID}
            serverStatus={srv.serverStatus}
            onTest={srv.handleTestPBSConnection}
            onEdit={srv.handleEditServer}
            onSetDefault={srv.handleSetDefaultPBS}
            onDelete={srv.handleDeletePBSServer}
          />

          {form(false)}
        </>
      )}

      <StatusNotice status={status} show={active} />
    </div>
  )
}
