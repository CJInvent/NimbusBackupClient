import { useTranslation } from '../../i18n/i18nContext'

// The configured storage servers, with their last connection test.
export default function ServerTable({ pbsServers, defaultPBSID, serverStatus, onTest, onEdit, onSetDefault, onDelete }) {
  const { t } = useTranslation()
  return (
    <div className="card" style={{marginBottom: '20px'}}>
      <h3>{t('configuredServers')} ({pbsServers.length})</h3>

      <table style={{width: '100%', marginTop: '15px'}}>
        <thead>
          <tr>
            <th>{t('name')}</th>
            <th>{t('url')}</th>
            <th>{t('datastore')}</th>
            <th>{t('status')}</th>
            <th>{t('actions')}</th>
          </tr>
        </thead>
        <tbody>
          {pbsServers.map(server => (
            <tr key={server.id}>
              <td>
                <strong>{server.name}</strong>
                {server.id === defaultPBSID && <span style={{marginLeft: '5px', color: 'var(--nc-warn)'}}>⭐ {t('default')}</span>}
                {server.description && <div style={{fontSize: '0.85em', color: 'var(--nc-text-dim)'}}>{server.description}</div>}
              </td>
              <td>{server.baseurl}</td>
              <td>{server.datastore}/{server.namespace || '-'}</td>
              <td>
                {serverStatus[server.id] === 'testing' && <span style={{color: 'var(--nc-accent)'}}>🔄 {t('statusTesting')}</span>}
                {serverStatus[server.id] === 'online' && <span style={{color: 'var(--nc-ok)'}}>🟢 {t('statusOnline')}</span>}
                {serverStatus[server.id] === 'offline' && <span style={{color: 'var(--nc-err)'}}>🔴 {t('statusOffline')}</span>}
                {!serverStatus[server.id] && <span style={{color: 'var(--nc-text-dim)'}}>⚪ {t('statusNotTested')}</span>}
              </td>
              <td>
                <button onClick={() => onTest(server.id)} style={{marginRight: '5px', padding: '5px 10px', fontSize: '0.9em'}}>
                  🔍 {t('testBtn')}
                </button>
                <button onClick={() => onEdit(server)} style={{marginRight: '5px', padding: '5px 10px', fontSize: '0.9em'}}>
                  ✏️ {t('editBtn')}
                </button>
                {server.id !== defaultPBSID && (
                  <button className="btn btn-warn" onClick={() => onSetDefault(server.id)} style={{marginRight: '5px', padding: '5px 10px', fontSize: '0.9em'}}>
                    ⭐ {t('setDefaultBtn')}
                  </button>
                )}
                <button className="btn btn-danger" onClick={() => onDelete(server.id)} style={{padding: '5px 10px', fontSize: '0.9em'}}>
                  🗑️ {t('deleteBtn')}
                </button>
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  )
}
