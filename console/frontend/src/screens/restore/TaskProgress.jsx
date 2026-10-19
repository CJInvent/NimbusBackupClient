import { useTranslation } from '../../i18n/i18nContext'
import { formatBytes, fmtRate, fmtEta } from '../../lib/format'

// The restore, scan or download in flight (restore:progress events).
export default function TaskProgress({ taskProg }) {
  const { t } = useTranslation()
  return (
    <div style={{marginTop: '12px'}}>
      <div className={taskProg.indet ? 'nc-taskbar indet' : 'nc-taskbar'}>
        <div className="nc-taskbar-fill" style={taskProg.indet ? {} : { width: `${taskProg.pct}%` }}/>
      </div>
      <p style={{display: 'flex', justifyContent: 'space-between', fontSize: '13px', color: 'var(--nc-text-dim)', marginTop: '4px', gap: '10px', flexWrap: 'wrap'}}>
        <span style={{overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap', maxWidth: '60%'}}>{taskProg.msg}</span>
        <span style={{fontVariantNumeric: 'tabular-nums'}}>
          {taskProg.total > 0 && `${formatBytes(taskProg.done)} / ${formatBytes(taskProg.total)} · `}
          {!taskProg.indet && `${taskProg.pct}%`}
          {fmtRate(taskProg.bps) && ` · ${fmtRate(taskProg.bps)}`}
          {taskProg.eta != null && ` · ${fmtEta(taskProg.eta)} ${t('timeLeft')}`}
        </span>
      </p>
    </div>
  )
}
