import { useTranslation } from '../../i18n/i18nContext'

// Exchange-aware backup and log truncation, shown when Exchange is installed.
export default function ExchangeOptions({ config, setConfig, exchangeStatus, exchangeLogMode }) {
  const { t } = useTranslation()
  return (
    <div className="form-group" style={exchangeStatus.highlight_setting ? {marginTop:'16px',background:'var(--nc-warn-bg)',border:'1px solid var(--nc-warn-border)',borderRadius:'6px',padding:'10px 14px'} : {marginTop:'16px'}}>
      <label style={{fontWeight:'bold'}}>{t('exchangeSection')} {exchangeStatus.version ? '(' + exchangeStatus.version + ')' : ''}</label>
      <div style={{fontSize:'0.85em',color:'var(--nc-text-dim)',margin:'4px 0 8px'}}>
        {exchangeStatus.highlight_setting ? '⚠️ ' + t('exchangeDetectedHint') : t('exchangeHint')}
      </div>
      <label>
        <input type="checkbox" checked={!!config.exchange_aware} onChange={(e) => setConfig({...config, exchange_aware: e.target.checked})} />
        {t('exchangeAwareLabel')}
      </label>
      <div style={exchangeLogMode.queried && exchangeLogMode.logs_accumulate && !config.exchange_log_truncation ? {marginTop:'10px',background:'var(--nc-warn-bg)',border:'1px solid var(--nc-warn-border)',borderRadius:'6px',padding:'8px 12px'} : {marginTop:'10px'}}>
        <label>
          <input type="checkbox" checked={!!config.exchange_log_truncation} onChange={(e) => setConfig({...config, exchange_log_truncation: e.target.checked})} />
          {t('exchangeLogTruncationLabel')}
        </label>
        <div style={{fontSize:'0.82em',color:'var(--nc-text-dim)',marginTop:'4px'}}>
          {t('exchangeLogTruncationHint')}
          {exchangeLogMode.queried && (
            <div style={{marginTop:'4px'}}>
              {exchangeLogMode.logs_accumulate ? '⚠️ ' + t('exchangeLogsAccumulate') : '✓ ' + t('exchangeLogsCircular')}
              {exchangeLogMode.detail ? ' — ' + exchangeLogMode.detail : ''}
            </div>
          )}
        </div>
      </div>
    </div>
  )
}
