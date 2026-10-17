import { useTranslation } from '../../i18n/i18nContext'

// Shown while no storage server is configured.
export default function ServerWelcome({ config, appVersion }) {
  const { t } = useTranslation()
  return (
    <div className="info-box" style={{marginBottom: '20px', backgroundColor: 'color-mix(in srgb, var(--nc-accent) 8%, var(--nc-panel-head))', borderLeft: '4px solid var(--nc-accent)'}}>
      👋 <strong>{t('welcomeMessage')}</strong> {t('welcomeText')}<br/>
      {!config.baseurl && (
        <>
          <br/>
          <strong>📦 {t('noPBSYet')}</strong><br/>
          <a
            href={`${t('chooseBackupUrl')}?utm_source=NimbusGui&utm_medium=tooling&utm_campaign=version-${appVersion}&utm_content=first-setup`}
            target="_blank"
            rel="noopener noreferrer"
            style={{color: 'var(--nc-accent)', fontWeight: 'bold', textDecoration: 'underline'}}
          >
            {t('orderStorage')} →
          </a>
        </>
      )}
    </div>
  )
}
