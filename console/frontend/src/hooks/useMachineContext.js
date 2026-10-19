import { useState, useEffect } from 'react'
import { GetControlServerStatus, GetStorageIdentityStatus, ApproveStorageIdentity, IsReadOnly } from '../lib/bindings'
import usePolling from './usePolling'

// What this machine is, as the service reports it: the control server's
// status (with the storage identity) and whether the console is locked. Both
// are asked at mount and every 30 s. docs/V4-GUI-AUDIT.md section 2 derives
// the machine's states from these two; S5 is where the screens use them.
//
// onStatus is called with every status read by the poll (not with changes
// made by a save or an approval), which is when the control-server form
// fills in an address it does not have yet.
export default function useMachineContext(onStatus) {
  const [cpStatus, setCpStatus] = useState(null)
  usePolling(async (alive) => {
    try {
      const st = await GetControlServerStatus()
      if (GetStorageIdentityStatus) st.storage_identity = await GetStorageIdentityStatus()
      if (!alive()) return
      setCpStatus(st)
      onStatus(st)
    } catch (e) { /* service unreachable — keep last known */ }
  }, 30000, !!GetControlServerStatus)

  // Is this console a status panel? Asked once at mount and then on the same
  // slow cadence as everything else, because an org locking a machine should
  // not need the technician to restart the app for it to take effect.
  //
  // PRESENTATION ONLY. The service refuses every mutating local-API call while
  // locked (gui/api/readonly.go), so a build that ignored this — or a modified
  // one that never asked — would still get 403s. What this changes is whether
  // the controls are in the markup at all: absent, not disabled, matching the
  // portal's no-leak rule. A greyed-out Start button still tells the user the
  // capability exists and invites them to find out why it is off.
  //
  // null means "not asked yet".
  const [readOnly, setReadOnly] = useState(null)
  const canAsk = !!IsReadOnly
  useEffect(() => { if (!canAsk) setReadOnly(false) }, [])
  usePolling(async (alive) => {
    try {
      const v = await IsReadOnly()
      if (alive()) setReadOnly(!!v)
    } catch {
      // Unreachable service is not a locked one. Leaving the full UI up
      // is safe: every button it draws will 403 until the service answers.
      if (alive()) setReadOnly(false)
    }
  }, 30000, canAsk)

  // Approve the observed disks (standalone), then show the service's new
  // answer. The panel renders; this is where it reaches the service.
  const approveStorage = async (input) => {
    await ApproveStorageIdentity(input)
    const st = await GetStorageIdentityStatus()
    setCpStatus(s => ({ ...s, storage_identity: st }))
  }

  return { cpStatus, setCpStatus, readOnly, approveStorage }
}
