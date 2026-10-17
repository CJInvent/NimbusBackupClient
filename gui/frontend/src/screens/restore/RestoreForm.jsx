import { useTranslation } from '../../i18n/i18nContext'
import { inPlaceBlocker, crossHostMismatch } from '../../lib/snapshots'
import RestoreModePicker from './RestoreModePicker'
import InPlaceWarning from './InPlaceWarning'
import RestoreDestination from './RestoreDestination'
import RestoreOptions from './RestoreOptions'
import RestoreButtons from './RestoreButtons'
import TaskProgress from './TaskProgress'

// Restore mode picker + destination + options + restore button
export default function RestoreForm({ r, hostname, systemInfo }) {
  const { t } = useTranslation()
  const blocker = inPlaceBlocker(r.snapshotMeta, systemInfo)
  const isInPlace = r.restoreMode === 'original'
  const crossHost = isInPlace && crossHostMismatch(r.snapshotMeta, hostname)
  const blockerTooltip = blocker ? (t(blocker) || '') : ''
  return (
    <div style={{marginTop: '20px'}}>
      {/* Mode picker */}
      <RestoreModePicker r={r} blocker={blocker} blockerTooltip={blockerTooltip} />

      {isInPlace && <InPlaceWarning r={r} crossHost={crossHost} hostname={hostname} />}

      {!isInPlace && <RestoreDestination r={r} />}

      <RestoreOptions r={r} isInPlace={isInPlace} />

      <RestoreButtons r={r} isInPlace={isInPlace} crossHost={crossHost} />

      {r.taskProg && <TaskProgress taskProg={r.taskProg} />}
    </div>
  )
}
