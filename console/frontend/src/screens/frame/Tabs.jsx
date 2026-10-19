import { useTranslation } from '../../i18n/i18nContext'

const TABS = [
  ['servers', 'tabServers'],
  ['backup', 'tabBackup'],
  ['restore', 'tabRestore'],
  ['about', 'tabAbout'],
]

export default function Tabs({ activeTab, onSelect }) {
  const { t } = useTranslation()
  return (
    <div className="tabs">
      {TABS.map(([id, label]) => (
        <div key={id} className={`tab ${activeTab === id ? 'active' : ''}`} onClick={() => onSelect(id)}>
          {t(label)}
        </div>
      ))}
    </div>
  )
}
