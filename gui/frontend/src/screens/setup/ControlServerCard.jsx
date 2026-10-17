import { useTranslation } from '../../i18n/i18nContext'

// The control server this machine reports to: its state, and the form that
// sets the address, the one-time enrollment token and the fingerprint.
export default function ControlServerCard({ cpStatus, cpForm, setCpForm, onSave }) {
  const { t } = useTranslation()
  return (
    <div className="card" style={{marginTop: '16px'}}>
      <label style={{fontWeight: 'bold'}}>{t('controlServerSection')}</label>
      <div className="hint-text" style={{margin: '4px 0 8px'}}>{t('controlServerHint')}</div>
      {cpStatus && cpStatus.configured && (
        <div className="cp-status">
          <span className={'cp-dot ' + (cpStatus.connected ? 'ok' : 'err')}></span>
          <span className="mono">{cpStatus.server_host}</span>
          <span>
            {cpStatus.connected ? t('controlServerConnected') : t('controlServerDisconnected')}
            {cpStatus.enrolled ? ` · ${t('controlServerAgentId')} ${cpStatus.agent_id}` : ` · ${t('controlServerNotEnrolled')}`}
          </span>
          {cpStatus.last_checkin && <span className="hint-text">{t('controlServerLastCheckin')}: {new Date(cpStatus.last_checkin).toLocaleString()}</span>}
          {!cpStatus.connected && cpStatus.last_error && <span className="hint-text">{cpStatus.last_error}</span>}
        </div>
      )}
      <label>{t('controlServerUrlLabel')}</label>
      <input type="text" placeholder="https://nimbus.example.com" value={cpForm.url} onChange={(e) => setCpForm({...cpForm, url: e.target.value})} />
      <label>{t('controlServerTokenLabel')}</label>
      <input type="password" placeholder={cpStatus && cpStatus.enrolled ? t('controlServerTokenEnrolledPh') : t('controlServerTokenPh')} value={cpForm.token} onChange={(e) => setCpForm({...cpForm, token: e.target.value})} />
      <label>{t('controlServerFpLabel')}</label>
      <input type="text" placeholder={t('controlServerFpPh')} value={cpForm.fp} onChange={(e) => setCpForm({...cpForm, fp: e.target.value})} />
      <button className="btn btn-secondary" style={{marginTop: '8px'}} onClick={onSave}>{t('controlServerSave')}</button>
    </div>
  )
}
