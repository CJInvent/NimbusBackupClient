import {
  ListPBSServers, GetDefaultPBSID, AddPBSServer, UpdatePBSServer, DeletePBSServer,
  SetDefaultPBSServer, TestPBSConnection, GetServerFingerprint, PinPBSServerFingerprint,
} from '../../lib/bindings'
import { emptyServerForm } from '../../hooks/usePbsServers'

// True when a connection failure is an unverified-certificate error (self-signed
// PBS with no fingerprint pinned). These are recoverable via trust-on-first-use.
// The service says so with NB-1011 (the console's TestPBSConnection); this
// used to match words in the TLS error.
const isCertError = (err) => /\[NB-1011\]/.test(String(err))

// The storage-server handlers, made per render over the current state
// (s: usePbsServers) as they were when they lived in App.jsx.
export default function makeServerActions(s, showStatus, t) {
  const loadPBSServers = async () => {
    try {
      if (ListPBSServers) {
        const servers = await ListPBSServers()
        s.setPbsServers(servers || [])
      }
      if (GetDefaultPBSID) {
        const defaultID = await GetDefaultPBSID()
        s.setDefaultPBSID(defaultID || '')
      }
    } catch (err) {
      console.error('Failed to load PBS servers:', err)
      showStatus(`❌ ${t('statusServerLoadError')} ${err}`, 'error')
    }
  }

  const handleAddPBSServer = async () => {
    if (!AddPBSServer) {
      showStatus('❌ ' + t('errNoRuntime'), 'error')
      return
    }

    try {
      // Generate ID from name if not provided
      if (!s.serverFormData.id) {
        s.serverFormData.id = s.serverFormData.name.toLowerCase().replace(/[^a-z0-9]/g, '-')
      }

      await AddPBSServer(s.serverFormData)
      showStatus(`✅ ${t('statusServerAdded')}`, 'success')

      // Reset form and reload
      s.setServerFormData(emptyServerForm())
      s.setEditingServer(null)
      await loadPBSServers()
    } catch (err) {
      showStatus(`❌ ${t('errPrefix')}: ${err}`, 'error')
    }
  }

  const handleUpdatePBSServer = async () => {
    if (!UpdatePBSServer) {
      showStatus('❌ ' + t('errNoRuntime'), 'error')
      return
    }

    try {
      await UpdatePBSServer(s.serverFormData)
      showStatus(`✅ ${t('statusServerUpdated')}`, 'success')

      // Reset form and reload
      s.setServerFormData(emptyServerForm())
      s.setEditingServer(null)
      await loadPBSServers()
    } catch (err) {
      showStatus(`❌ ${t('errPrefix')}: ${err}`, 'error')
    }
  }

  const handleDeletePBSServer = async (id) => {
    if (!DeletePBSServer) {
      showStatus('❌ ' + t('errNoRuntime'), 'error')
      return
    }

    if (!confirm(t('confirmDeleteServer').replace('{id}', id))) {
      return
    }

    try {
      await DeletePBSServer(id)
      showStatus(`✅ ${t('statusServerDeleted')}`, 'success')
      await loadPBSServers()
    } catch (err) {
      showStatus(`❌ ${t('errPrefix')}: ${err}`, 'error')
    }
  }

  const handleSetDefaultPBS = async (id) => {
    if (!SetDefaultPBSServer) {
      showStatus('❌ ' + t('errNoRuntime'), 'error')
      return
    }

    try {
      await SetDefaultPBSServer(id)
      s.setDefaultPBSID(id)
      showStatus(`✅ ${t('statusServerSetDefault').replace('{id}', id)}`, 'success')
    } catch (err) {
      showStatus(`❌ ${t('errPrefix')}: ${err}`, 'error')
    }
  }

  const handleTestPBSConnection = async (id) => {
    if (!TestPBSConnection) {
      showStatus('❌ ' + t('errNoRuntime'), 'error')
      return
    }

    try {
      s.setServerStatus(prev => ({ ...prev, [id]: 'testing' }))
      await TestPBSConnection(id)
      s.setServerStatus(prev => ({ ...prev, [id]: 'online' }))
      showStatus(`✅ ${t('statusConnectionSuccess').replace('{id}', id)}`, 'success')
    } catch (err) {
      const server = s.pbsServers.find(x => x.id === id)
      // Self-signed cert + no fingerprint yet: offer to discover and pin it (TOFU).
      if (isCertError(err) && server && !(server.certfingerprint || '').trim() &&
          GetServerFingerprint && PinPBSServerFingerprint) {
        try {
          const fp = await GetServerFingerprint(server.baseurl)
          if (window.confirm(t('tofuConfirm').replace('{host}', server.baseurl).replace('{fp}', fp))) {
            await PinPBSServerFingerprint(id, fp)
            await loadPBSServers()
            await TestPBSConnection(id)
            s.setServerStatus(prev => ({ ...prev, [id]: 'online' }))
            showStatus(`✅ ${t('statusFingerprintPinned')}`, 'success')
            return
          }
        } catch (fpErr) {
          showStatus(`❌ ${t('statusFingerprintFailed')} ${fpErr}`, 'error')
          s.setServerStatus(prev => ({ ...prev, [id]: 'offline' }))
          return
        }
      }
      s.setServerStatus(prev => ({ ...prev, [id]: 'offline' }))
      showStatus(`❌ ${t('statusConnectionFailed')} ${err}`, 'error')
    }
  }

  const handleEditServer = (server) => {
    // A listed server carries no secret at all (the service's list cannot),
    // so the form starts from the empty one: the secret field is '' and an
    // unchanged form keeps the stored token.
    s.setServerFormData({ ...emptyServerForm(), ...server })
    s.setEditingServer(server.id)
  }

  const handleCancelEdit = () => {
    s.setServerFormData(emptyServerForm())
    s.setEditingServer(null)
  }

  return {
    handleAddPBSServer, handleUpdatePBSServer, handleDeletePBSServer, handleSetDefaultPBS,
    handleTestPBSConnection, handleEditServer, handleCancelEdit,
  }
}
