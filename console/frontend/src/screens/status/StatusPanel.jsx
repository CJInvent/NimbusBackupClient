import useStatusPanel from '../../hooks/useStatusPanel'
import StatusPanelView from './StatusPanelView'

/*
 * StatusPanel — what a locked console shows, and the only thing it shows.
 *
 * Rendered INSTEAD OF the tabbed UI when the org sets `gui_read_only`, not
 * alongside it with the controls disabled. That is deliberate and matches the
 * portal's View::cap rule: a denied control is not in the markup. A greyed-out
 * Start button still tells a user the capability exists and invites them to
 * find out why it is off.
 *
 * This is PRESENTATION ONLY. The control is the service refusing every
 * mutating local-API call while locked (gui/api/readonly.go) — so a modified
 * front end that renders the old UI anyway gets 403s, and nothing here is
 * load-bearing for security. What it is load-bearing for is the technician
 * standing at the machine being able to answer "is this thing working?"
 * without calling the MSP.
 *
 * Everything is derived from three polled endpoints (hooks/useStatusPanel).
 */
export default function StatusPanel({ version, hostname }) {
  const data = useStatusPanel()
  return <StatusPanelView version={version} hostname={hostname} {...data} />
}
