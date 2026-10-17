import { useTranslation } from '../../i18n/i18nContext'

// Add or edit a storage server. `first` is the form shown while none is
// configured: its own title and button, and the API-token tip.
export default function ServerForm({ first, editingServer, serverFormData, setServerFormData, onAdd, onUpdate, onCancel }) {
  const { t } = useTranslation()
  const set = (field) => (e) => setServerFormData({...serverFormData, [field]: e.target.value})
  return (
    <div className="card">
      <h3>{editingServer ? `✏️ ${t('editServer')}` : `➕ ${t(first ? 'addYourServer' : 'addAnotherServer')}`}</h3>

      <div className="form-group">
        <label>{t('serverName')}</label>
        <input
          type="text"
          value={serverFormData.name}
          onChange={set('name')}
          placeholder={t('phServerName')}
        />
      </div>

      {!editingServer && (
        <div className="form-group">
          <label>{t('serverID')}</label>
          <input
            type="text"
            value={serverFormData.id}
            onChange={set('id')}
            placeholder={t('phServerIdAuto')}
          />
        </div>
      )}

      <div className="form-group">
        <label>{t('serverURL')}</label>
        <input
          type="text"
          value={serverFormData.baseurl}
          onChange={set('baseurl')}
          placeholder="https://pbs-ssd.example.com:8007"
        />
      </div>

      <div className="form-group">
        <label>{t('authID')}</label>
        <input
          type="text"
          value={serverFormData.authid}
          onChange={set('authid')}
          placeholder="backup@pbs!token-name"
        />
      </div>

      <div className="form-group">
        <label>{t('secret')}</label>
        <input
          type="password"
          value={serverFormData.secret}
          onChange={set('secret')}
          placeholder={serverFormData.secret_set ? '•••••••• (laisser vide pour conserver le token actuel)' : 'xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx'}
        />
      </div>

      <div className="form-group">
        <label>{t('datastore')}</label>
        <input
          type="text"
          value={serverFormData.datastore}
          onChange={set('datastore')}
          placeholder="ssd-fast"
        />
      </div>

      <div className="form-group">
        <label>{t('namespace')}</label>
        <input
          type="text"
          value={serverFormData.namespace}
          onChange={set('namespace')}
          placeholder="clients"
        />
      </div>

      <div className="form-group">
        <label>{t('certFingerprint')}</label>
        <input
          type="text"
          value={serverFormData.certfingerprint}
          onChange={set('certfingerprint')}
          placeholder="AA:BB:CC:DD:..."
        />
      </div>

      <div className="form-group">
        <label>{t('description')}</label>
        <textarea
          value={serverFormData.description}
          onChange={set('description')}
          placeholder={t('phServerComment')}
          rows="2"
        />
      </div>

      <div style={{display: 'flex', gap: '10px', marginTop: '20px'}}>
        {editingServer ? (
          <>
            <button onClick={onUpdate} style={{flex: 1}}>
              💾 {t('update')}
            </button>
            <button className="btn btn-secondary" onClick={onCancel} style={{flex: 1}}>
              ❌ {t('cancel')}
            </button>
          </>
        ) : (
          <button onClick={onAdd} style={{flex: 1}}>
            ➕ {t(first ? 'addFirstServer' : 'addServer')}
          </button>
        )}
      </div>

      {first && (
        <div className="info-box" style={{marginTop: '20px'}}>
          💡 <strong>{t('tipTitle')}</strong> {t('tipAPIToken')}<br/>
          {t('tipAPITokenPath')}
        </div>
      )}
    </div>
  )
}
