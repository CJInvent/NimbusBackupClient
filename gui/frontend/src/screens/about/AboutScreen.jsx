import { useTranslation } from '../../i18n/i18nContext'
import AboutLists from './AboutLists'

// The About tab. (Its upstream vendor's logo, links and wording go in S3,
// docs/V4-GUI-AUDIT.md section 5.)
export default function AboutScreen({ active, appVersion }) {
  const { t } = useTranslation()
  return (
    <div className={`tab-content ${active ? 'active' : ''}`}>
      <h2 style={{textAlign: 'center'}}>{t('aboutTitle')}</h2>

      <img
        src="https://nimbus.rdem-systems.com/logo.webp"
        alt="Nimbus Backup"
        className="logo"
        onError={(e) => e.target.style.display = 'none'}
      />

      <div style={{textAlign: 'center', marginTop: '30px'}}>
        <h3>Nimbus Backup</h3>
        <p style={{color: 'var(--nc-text-dim)', margin: '10px 0'}}>{t('version')} {appVersion}</p>

        {/* Upsell CTA */}
        <div style={{margin: '20px 0'}}>
          <a
            href={`${t('chooseBackupUrl')}?utm_source=NimbusGui&utm_medium=tooling&utm_campaign=version-${appVersion}&utm_content=version-${appVersion}`}
            target="_blank"
            rel="noopener noreferrer"
            style={{
              display: 'inline-block',
              padding: '12px 24px',
              backgroundColor: 'var(--nc-accent)',
              color: 'var(--nc-accent-text)',
              textDecoration: 'none',
              borderRadius: 'var(--nc-radius)',
              fontWeight: 'bold',
              transition: 'filter 0.2s'
            }}
            onMouseEnter={(e) => e.target.style.filter = 'brightness(1.1)'}
            onMouseLeave={(e) => e.target.style.filter = ''}
          >
            📦 {t('orderStorageCTA')}
          </a>
        </div>

        <AboutLists />

        <p style={{marginTop: '30px'}}>
          <strong>{t('copyright')}</strong><br/>
          <a href="https://nimbus.rdem-systems.com" style={{color: 'var(--nc-accent)'}}>nimbus.rdem-systems.com</a>
        </p>

        <p style={{marginTop: '20px', color: 'var(--nc-text-dim)', fontSize: '12px'}}>
          {t('basedOn')}<br/>
          {t('techStack')}
        </p>
      </div>
    </div>
  )
}
