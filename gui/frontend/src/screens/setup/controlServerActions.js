import { SaveControlServerConfig, GetControlServerStatus } from '../../lib/bindings'

// Save the control-server form (address, one-time token, fingerprint) and
// read the status back.
export default function makeSaveControlServer({ cpForm, setCpForm, setCpStatus }, showStatus, t) {
  return async () => {
    if (!SaveControlServerConfig) return
    try {
      await SaveControlServerConfig((cpForm.url || '').trim(), (cpForm.token || '').trim(), (cpForm.fp || '').trim())
      setCpForm(f => ({ ...f, token: '' })) // one-time token never lingers in the form
      showStatus('✅ ' + t('controlServerSaved'), 'success')
      const st = await GetControlServerStatus()
      setCpStatus(st)
    } catch (e) {
      showStatus('❌ ' + (e && e.message ? e.message : String(e)), 'error')
    }
  }
}
