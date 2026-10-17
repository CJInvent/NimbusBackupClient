import { useState } from 'react'
import { localizeMessage } from '../lib/messages'

// The one status line each tab shows at its foot, for five seconds. S7
// replaces it with the notices of docs/V4-GUI-AUDIT.md section 4.3.
export default function useStatusMessage(t) {
  const [status, setStatus] = useState({ message: '', type: '', visible: false })
  const showStatus = (message, type) => {
    setStatus({ message: localizeMessage(t, message), type, visible: true })
    setTimeout(() => {
      setStatus(s => ({ ...s, visible: false }))
    }, 5000)
  }
  return { status, showStatus }
}
