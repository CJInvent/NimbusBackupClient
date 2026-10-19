import { useState, useEffect } from 'react'
import { GetVersion, GetSystemInfo, GetLastBackupDirs, GetConfigWithHostname } from '../lib/bindings'

// What the window reads once at mount: the version, the machine (mode, admin
// status, service availability), the saved configuration with the machine's
// hostname, and the folders of the last backup to pre-fill the form
// (setBackupDirs, owned by the backup form).
export default function useAppData(setBackupDirs) {
  const [hostname, setHostname] = useState('')
  const [appVersion, setAppVersion] = useState('dev')
  const [systemInfo, setSystemInfo] = useState({ mode: 'Standalone', is_admin: false, service_available: false, os: '' })
  const [config, setConfig] = useState({
    baseurl: '',
    certfingerprint: '',
    authid: '',
    secret: '',
    datastore: '',
    namespace: '',
    backupdir: '',
    'backup-id': '',
    usevss: true
  })

  useEffect(() => {
    const loadData = async () => {
      try {
        // Load version
        if (GetVersion) {
          const version = await GetVersion()
          setAppVersion(version || 'dev')
        }

        // Load system info (mode, admin status, service availability)
        if (GetSystemInfo) {
          const sysInfo = await GetSystemInfo()
          setSystemInfo(sysInfo || { mode: 'Standalone', is_admin: false, service_available: false })
        }

        // Load last backup directories to pre-fill the form
        if (GetLastBackupDirs) {
          const lastDirs = await GetLastBackupDirs()
          if (lastDirs && lastDirs.length > 0) {
            setBackupDirs(lastDirs.join('\n'))
          }
        }

        if (GetConfigWithHostname) {
          const data = await GetConfigWithHostname()
          if (data) {
            // Extract hostname
            const hn = data.hostname || ''
            setHostname(hn)

            // Set config (hostname is already in backup-id if needed)
            setConfig({
              baseurl: data.baseurl || '',
              certfingerprint: data.certfingerprint || '',
              authid: data.authid || '',
              secret: data.secret || '',
              datastore: data.datastore || '',
              namespace: data.namespace || '',
              backupdir: data.backupdir || '',
              'backup-id': data['backup-id'] || hn,
              usevss: data.usevss !== undefined ? data.usevss : true,
              upload_limit_mbps: data.upload_limit_mbps || 0,
              exchange_aware: data.exchange_aware || false,
              exchange_log_truncation: data.exchange_log_truncation || false
            })

            // Initialize backupDirs from config if available
            if (data.backupdir) {
              setBackupDirs(data.backupdir)
            }
          }
        }
      } catch (err) {
        console.error('Failed to load config:', err)
      }
    }

    loadData()
  }, [])

  return { hostname, appVersion, systemInfo, config, setConfig }
}
