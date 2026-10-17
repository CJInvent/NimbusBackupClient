import { useTranslation } from '../../i18n/i18nContext'

export default function ConnectionTile({ title, state, lines }) {
  const { t } = useTranslation()
  const label = state === 'ok' ? t('panelOnline')
    : state === 'down' ? t('panelOffline')
      : t('panelUnknown')
  const icon = state === 'ok' ? '●' : state === 'down' ? '●' : '○'

  return (
    <div className={`panel-tile panel-tile-${state}`}>
      <div className="panel-tile-head">
        <span className="panel-tile-dot" aria-hidden="true">{icon}</span>
        <span className="panel-tile-title">{title}</span>
        <span className="panel-tile-state">{label}</span>
      </div>
      {lines.filter(Boolean).map((l, i) => (
        <div className="panel-tile-line" key={i}>{l}</div>
      ))}
    </div>
  )
}
