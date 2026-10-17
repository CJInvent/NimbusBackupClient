import { useTranslation } from '../../i18n/i18nContext'

export default function SecurityWarnings({ warnings }) {
  const { t } = useTranslation()
  if (warnings.length === 0) return null
  return (
    <div className="security-warning-banner" style={{background:'var(--nc-warn-bg)',border:'1px solid var(--nc-warn-border)',borderRadius:'6px',padding:'10px 14px',margin:'8px'}}>
      <strong>⚠️ {t('securityWarningTitle')}</strong>
      <ul style={{margin:'6px 0 0',paddingLeft:'20px'}}>
        {warnings.map((w,i) => <li key={i} style={{fontSize:'0.9em'}}>{w}</li>)}
      </ul>
    </div>
  )
}
