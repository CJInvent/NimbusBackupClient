// The status line at the foot of the active tab (useStatusMessage).
export default function StatusNotice({ status, show }) {
  if (!(status.visible && show)) return null
  return <div className={`status ${status.type} visible`}>{status.message}</div>
}
