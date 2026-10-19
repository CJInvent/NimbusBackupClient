import { useTranslation } from '../../i18n/i18nContext'
import HeaderControls from '../../components/HeaderControls'

export default function Header() {
  const { t } = useTranslation()
  return (
    <div className="header">
      <div style={{display: 'flex', justifyContent: 'space-between', alignItems: 'center'}}>
        <div>
          <h1>🛡️ {t('appTitle')}</h1>
          <p>{t('appSubtitle')}</p>
        </div>
        <HeaderControls />
      </div>
    </div>
  )
}
