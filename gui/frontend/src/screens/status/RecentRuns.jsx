import { useTranslation } from '../../i18n/i18nContext'
import { fmtDuration, fmtWhen, rollupByDay } from './statusFormat'

// A machine backing up hourly has ~168 runs in a week. The table shows a
// per-day rollup with the worst outcome of each day, and the most recent runs
// in full — a flat list of 168 rows is not something anyone reads.
const DETAIL_ROWS = 10

export default function RecentRuns({ recent }) {
  const { t } = useTranslation()
  const days = rollupByDay(recent)
  const detail = recent.slice(0, DETAIL_ROWS)
  return (
    <section className="panel-section">
      <h2 className="panel-h2">{t('panelSevenDays')}</h2>
      {days.length === 0 ? (
        <div className="panel-empty">{t('panelNoRuns')}</div>
      ) : (
        <div className="panel-days">
          {days.map(d => (
            <div className={`panel-day ${d.running ? 'is-running' : d.failed ? 'is-failed' : 'is-ok'}`} key={d.key}>
              <div className="panel-day-date">{d.key}</div>
              <div className="panel-day-count">
                {d.failed > 0
                  ? t('panelDayFailed').replace('{failed}', d.failed).replace('{total}', d.total)
                  : t('panelDayOk').replace('{total}', d.total)}
              </div>
            </div>
          ))}
        </div>
      )}

      {detail.length > 0 && (
        <table className="panel-table">
          <thead>
            <tr>
              <th>{t('panelWhen')}</th>
              <th>{t('panelJob')}</th>
              <th>{t('panelTrigger')}</th>
              <th>{t('panelDuration')}</th>
              <th>{t('panelResult')}</th>
            </tr>
          </thead>
          <tbody>
            {detail.map(r => (
              <tr key={r.run_id}>
                <td>{fmtWhen(r.started_at)}</td>
                <td>{r.job_name || r.backup_id || '—'}</td>
                <td>{r.trigger || '—'}</td>
                <td>{r.state !== 'done'
                  ? t('panelInProgress')
                  : fmtDuration((Date.parse(r.ended_at) - Date.parse(r.started_at)) / 1000)}</td>
                <td className={r.state !== 'done' ? 'is-running' : r.success ? 'is-ok' : 'is-failed'}>
                  {r.state !== 'done'
                    ? t('panelInProgress')
                    : r.success ? t('panelSucceeded') : (r.error || t('panelFailed'))}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </section>
  )
}
