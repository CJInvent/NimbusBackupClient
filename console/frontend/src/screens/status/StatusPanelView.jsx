import { useTranslation } from '../../i18n/i18nContext'
import ConnectionTile from './ConnectionTile'
import LiveRun from './LiveRun'
import RecentRuns from './RecentRuns'
import { fmtWhen, reachabilityState } from './statusFormat'

// The status page itself, from what useStatusPanel read.
export default function StatusPanelView({ version, hostname, conns, active, recent, serviceUp }) {
  const { t } = useTranslation()
  const pbs = (conns && conns.pbs) || []
  const cp = (conns && conns.control_plane) || {}

  return (
    <div className="panel">
      <header className="panel-head">
        <div>
          <h1 className="panel-title">{t('panelTitle')}</h1>
          <div className="panel-sub">{hostname || ''}{version ? ` · ${version}` : ''}</div>
        </div>
        <div className="panel-managed">{t('panelManagedNotice')}</div>
      </header>

      <section className="panel-section">
        <h2 className="panel-h2">{t('panelConnections')}</h2>
        <div className="panel-tiles">
          <ConnectionTile
            title={t('panelService')}
            state={serviceUp ? 'ok' : 'down'}
            lines={[serviceUp ? t('panelServiceRunning') : t('panelServiceDown')]}
          />
          <ConnectionTile
            title={t('panelControlServer')}
            state={!cp.configured ? 'unknown' : cp.connected ? 'ok' : 'down'}
            lines={[
              cp.configured ? cp.server_host : t('panelNotManaged'),
              cp.configured && cp.last_success ? `${t('panelLastCheckin')} ${fmtWhen(cp.last_success)}` : null,
              cp.configured && cp.last_error ? cp.last_error : null,
            ]}
          />
          {pbs.map(p => (
            <ConnectionTile
              key={p.id}
              title={p.name || p.id}
              state={reachabilityState(p.reachable)}
              lines={[
                p.host,
                p.datastore,
                p.checked_at ? `${t('panelChecked')} ${fmtWhen(p.checked_at)}` : t('panelNeverChecked'),
              ]}
            />
          ))}
          {pbs.length === 0 ? <div className="panel-empty">{t('panelNoDestinations')}</div> : null}
        </div>
      </section>

      <section className="panel-section">
        <h2 className="panel-h2">{t('panelRunningNow')}</h2>
        <LiveRun run={active} serverTime={conns && conns.server_time} />
      </section>

      <RecentRuns recent={recent} />
    </div>
  )
}
