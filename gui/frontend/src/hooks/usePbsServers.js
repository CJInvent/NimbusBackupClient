import { useState, useEffect } from 'react'
import { ListPBSServers, GetDefaultPBSID } from '../lib/bindings'

export const emptyServerForm = () => ({
  id: '',
  name: '',
  baseurl: '',
  certfingerprint: '',
  authid: '',
  secret: '',
  datastore: '',
  namespace: '',
  description: ''
})

// The storage servers this machine knows (standalone setup), the default
// one, the add/edit form, and each server's last connection test. The
// handlers are screens/setup/serverActions.js.
export default function usePbsServers() {
  const [pbsServers, setPbsServers] = useState([])
  const [defaultPBSID, setDefaultPBSID] = useState('')
  const [editingServer, setEditingServer] = useState(null)
  const [serverFormData, setServerFormData] = useState(emptyServerForm())
  const [serverStatus, setServerStatus] = useState({}) // Map of server ID -> connection status

  // Load PBS servers on mount
  useEffect(() => {
    const loadPBSServers = async () => {
      try {
        if (ListPBSServers) {
          const servers = await ListPBSServers()
          setPbsServers(servers || [])
        }

        if (GetDefaultPBSID) {
          const defaultID = await GetDefaultPBSID()
          setDefaultPBSID(defaultID || '')
        }
      } catch (err) {
        console.error('Failed to load PBS servers:', err)
      }
    }

    loadPBSServers()
  }, [])

  return {
    pbsServers, setPbsServers, defaultPBSID, setDefaultPBSID,
    editingServer, setEditingServer, serverFormData, setServerFormData,
    serverStatus, setServerStatus,
  }
}
