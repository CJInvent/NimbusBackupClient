import { useTranslation } from '../../i18n/i18nContext'

export default function AboutLists() {
  const { t } = useTranslation()
  return (
    <div className="grid" style={{marginTop: '30px', textAlign: 'left'}}>
      <div className="card">
        <h3>✅ {t('features')}</h3>
        <ul style={{lineHeight: 2, marginLeft: '20px'}}>
          <li>{t('featuresList.directories')}</li>
          <li>{t('featuresList.machine')}</li>
          <li>{t('featuresList.restore')}</li>
          <li>{t('featuresList.vss')}</li>
          <li>{t('featuresList.dedup')}</li>
          <li>{t('featuresList.modern')}</li>
        </ul>
      </div>

      <div className="card">
        <h3>🚀 {t('technology')}</h3>
        <ul style={{lineHeight: 2, marginLeft: '20px'}}>
          <li>{t('techList.wails')}</li>
          <li>{t('techList.performance')}</li>
          <li>{t('techList.interface')}</li>
          <li>{t('techList.logs')}</li>
          <li>{t('techList.nogpu')}</li>
        </ul>
      </div>
    </div>
  )
}
