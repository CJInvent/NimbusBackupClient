import { useState } from 'react'

// The control-server form: address, one-time enrollment token, fingerprint.
// prefill takes a polled status and fills in the address when the form has
// none yet (useMachineContext calls it).
export default function useControlServerForm() {
  const [cpForm, setCpForm] = useState({ url: '', token: '', fp: '' })
  const prefill = (st) => {
    setCpForm(f => f.url === '' && st && st.server_host ? { ...f, url: (st.server_host.startsWith('http') ? st.server_host : 'https://' + st.server_host) } : f)
  }
  return { cpForm, setCpForm, prefill }
}
