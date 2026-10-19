import { useTranslation } from '../../i18n/i18nContext'

// BETA Warning
export default function RestoreBetaBox() {
  const { t } = useTranslation()
  return (
    <div className="warn-box" style={{marginBottom: '20px'}}>
      <strong>⚠️ {t('restoreBetaTitle')}</strong>
      <p style={{margin: '8px 0 0 0', fontSize: '14px'}}>
        {t('restoreBetaIntro')}
        <br/>✅ {t('restoreBetaFilesDirs')}
        <br/>✅ {t('restoreBetaSelective')}
        <br/>✅ {t('restoreBetaTimestamps')}
        <br/>❌ {t('restoreBetaACLs')}
      </p>
    </div>
  )
}
