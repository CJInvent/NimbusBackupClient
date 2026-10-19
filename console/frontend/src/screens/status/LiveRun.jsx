import { useTranslation } from '../../i18n/i18nContext'
import { fmtBytes, fmtDuration } from './statusFormat'

export default function LiveRun({ run, serverTime }) {
  const { t } = useTranslation()
  if (!run) return <div className="panel-idle">{t('panelNoRunNow')}</div>

  const started = Date.parse(run.started_at)
  const now = Date.parse(serverTime) || Date.now()
  const elapsed = Number.isNaN(started) ? null : (now - started) / 1000
  const pct = Math.max(0, Math.min(100, Math.round(run.percent || 0)))

  return (
    <div className="panel-live">
      <div className="panel-live-head">
        <span className="panel-live-name">{run.job_name || run.backup_id || t('panelBackup')}</span>
        <span className="panel-live-pct">{pct}%</span>
      </div>
      <div className="panel-bar"><div className="panel-bar-fill" style={{ width: `${pct}%` }} /></div>
      <div className="panel-live-msg">{run.message || ''}</div>
      <div className="panel-live-grid">
        <div><span>{t('panelTrigger')}</span> {run.trigger || '—'}</div>
        <div><span>{t('panelElapsed')}</span> {fmtDuration(elapsed)}</div>
        <div><span>{t('panelData')}</span> {fmtBytes(run.bytes_done)}{run.bytes_total ? ` / ${fmtBytes(run.bytes_total)}` : ''}</div>
        <div><span>{t('panelChunks')}</span> {(run.new_chunks || 0)} / {(run.reused_chunks || 0)}</div>
      </div>
      {run.current_dir ? <div className="panel-live-dir">{run.current_dir}</div> : null}
    </div>
  )
}
