import { useTranslation } from '../../i18n/i18nContext'
import ExchangeOptions from './ExchangeOptions'

// Backup ID, VSS, the upload limit, and Exchange when it is installed.
export default function BackupSettings({ config, setConfig, exchangeStatus, exchangeLogMode }) {
  const { t } = useTranslation()
  return (
    <>
      <div className="form-group">
        <label>{t('backupID')}</label>
        <input
          type="text"
          value={config['backup-id']}
          onChange={(e) => setConfig({...config, 'backup-id': e.target.value})}
          placeholder={t('backupIDPlaceholder')}
        />
      </div>

      <div className="form-group">
        <label>
          <input
            type="checkbox"
            checked={config.usevss}
            onChange={(e) => setConfig({...config, usevss: e.target.checked})}
          />
          {t('useVSS')}
        </label>
        <div style={{marginTop: '12px'}}>
          <label>{t('uploadLimitLabel')}</label>
          <input
            type="number"
            min="0"
            step="1"
            value={config.upload_limit_mbps || 0}
            onChange={(e) => setConfig({...config, upload_limit_mbps: e.target.value})}
          />
          <div style={{fontSize: '0.85em', color: 'var(--nc-text-dim)', marginTop: '4px'}}>{t('uploadLimitHint')}</div>
        </div>
        {exchangeStatus.installed && (
          <ExchangeOptions config={config} setConfig={setConfig} exchangeStatus={exchangeStatus} exchangeLogMode={exchangeLogMode} />
        )}
      </div>
    </>
  )
}
