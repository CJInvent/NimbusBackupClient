import { useState, useEffect, useMemo } from 'react'
import { useTranslation } from './i18n/i18nContext'

import HeaderControls from './components/HeaderControls'
import PathPicker from './components/PathPicker'

// Wails runtime imports (will be available when built with Wails)
let GetConfigWithHostname, SaveConfig, TestConnection, StartBackup, ListSnapshots, ListSnapshotContents, GetSnapshotMeta, RestoreSnapshot, ListPhysicalDisks, GetVersion, EventsOn, SearchFiles, CancelSearch, GetControlServerStatus, SaveControlServerConfig, SetTrayLanguage, CheckDownloadSpace, DownloadSelection, ListImageContents, DownloadImageSelection, LastImageListTruncated, ListImagePartitions, RestoreImageSelection, ListDrives, ListFolders, CreateFolder, DefaultSaveDir
let SaveScheduledJob, UpdateScheduledJob, GetScheduledJobs, DeleteScheduledJob, GetJobHistory, GetSystemInfo, GetLastBackupDirs, GetSecurityWarnings, GetExchangeStatus, QueryExchangeLogMode
// Multi-PBS functions
let ListPBSServers, GetPBSServer, AddPBSServer, UpdatePBSServer, DeletePBSServer, SetDefaultPBSServer, GetDefaultPBSID, TestPBSConnection
let GetServerFingerprint, PinPBSServerFingerprint

// Check if we're running in Wails
if (window.go) {
  GetConfigWithHostname = window.go.main.App.GetConfigWithHostname
  GetControlServerStatus = window.go.main.App.GetControlServerStatus
  SaveControlServerConfig = window.go.main.App.SaveControlServerConfig
  SetTrayLanguage = window.go.main.App.SetTrayLanguage
  CheckDownloadSpace = window.go.main.App.CheckDownloadSpace
  DownloadSelection = window.go.main.App.DownloadSelection
  ListImageContents = window.go.main.App.ListImageContents
  LastImageListTruncated = window.go.main.App.LastImageListTruncated
  ListImagePartitions = window.go.main.App.ListImagePartitions
  RestoreImageSelection = window.go.main.App.RestoreImageSelection
  ListDrives = window.go.main.App.ListDrives
  ListFolders = window.go.main.App.ListFolders
  CreateFolder = window.go.main.App.CreateFolder
  DefaultSaveDir = window.go.main.App.DefaultSaveDir
  DownloadImageSelection = window.go.main.App.DownloadImageSelection
  SaveConfig = window.go.main.App.SaveConfig
  TestConnection = window.go.main.App.TestConnection
  StartBackup = window.go.main.App.StartBackup
  ListSnapshots = window.go.main.App.ListSnapshots
  ListSnapshotContents = window.go.main.App.ListSnapshotContents
  GetSnapshotMeta = window.go.main.App.GetSnapshotMeta
  RestoreSnapshot = window.go.main.App.RestoreSnapshot
  SearchFiles = window.go.main.App.SearchFiles
  CancelSearch = window.go.main.App.CancelSearch
  ListPhysicalDisks = window.go.main.App.ListPhysicalDisks
  GetVersion = window.go.main.App.GetVersion
  GetSecurityWarnings = window.go.main.App.GetSecurityWarnings
  GetExchangeStatus = window.go.main.App.GetExchangeStatus
  QueryExchangeLogMode = window.go.main.App.QueryExchangeLogMode
  SaveScheduledJob = window.go.main.App.SaveScheduledJob
  UpdateScheduledJob = window.go.main.App.UpdateScheduledJob
  GetScheduledJobs = window.go.main.App.GetScheduledJobs
  DeleteScheduledJob = window.go.main.App.DeleteScheduledJob
  GetJobHistory = window.go.main.App.GetJobHistory
  GetSystemInfo = window.go.main.App.GetSystemInfo
  GetLastBackupDirs = window.go.main.App.GetLastBackupDirs
  // Multi-PBS
  ListPBSServers = window.go.main.App.ListPBSServers
  GetPBSServer = window.go.main.App.GetPBSServer
  AddPBSServer = window.go.main.App.AddPBSServer
  UpdatePBSServer = window.go.main.App.UpdatePBSServer
  DeletePBSServer = window.go.main.App.DeletePBSServer
  SetDefaultPBSServer = window.go.main.App.SetDefaultPBSServer
  GetDefaultPBSID = window.go.main.App.GetDefaultPBSID
  TestPBSConnection = window.go.main.App.TestPBSConnection
  GetServerFingerprint = window.go.main.App.GetServerFingerprint
  PinPBSServerFingerprint = window.go.main.App.PinPBSServerFingerprint
}

// Wails events
if (window.runtime) {
  EventsOn = window.runtime.EventsOn
}

function App() {
  const { t } = useTranslation()
  const [activeTab, setActiveTab] = useState('servers')
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

  // Multi-PBS states
  const [pbsServers, setPbsServers] = useState([])
  const [defaultPBSID, setDefaultPBSID] = useState('')
  const [selectedPBSID, setSelectedPBSID] = useState('')
  const [editingServer, setEditingServer] = useState(null)
  const [serverFormData, setServerFormData] = useState({
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
  const [serverStatus, setServerStatus] = useState({}) // Map of server ID -> connection status

  const [backupType, setBackupType] = useState('directory')
  // Volume (disk-image) backup detection: machine backups store one
  // *.img.fidx per disk (fixed-index raw images) instead of a pxar archive.
  const isVolumeSnapshot = (snap) => {
    if (!snap) return false
    const files = snap.files || []
    if (files.some(f => String(f).includes('.img.fidx'))) return true
    return snap.backup_type === 'vm' || snap.backup_type === 'machine'
  }
  const snapshotDisks = (snap) => (snap && snap.files ? snap.files.filter(f => String(f).includes('.img.fidx')) : [])
  const [lastClickedPath, setLastClickedPath] = useState(null) // shift-range anchor
  const [downloading, setDownloading] = useState(false)
  const [backupDirs, setBackupDirs] = useState('')
  const [selectedDrives, setSelectedDrives] = useState([])
  const [physicalDisks, setPhysicalDisks] = useState([])
  const [securityWarnings, setSecurityWarnings] = useState([])
  const [exchangeStatus, setExchangeStatus] = useState({installed:false, version:"", aware:false, highlight_setting:false, log_truncation:false, highlight_truncation:false})
  const [exchangeLogMode, setExchangeLogMode] = useState({queried:false, logs_accumulate:false, detail:""})
  const [disksLoading, setDisksLoading] = useState(false)
  const [disksError, setDisksError] = useState('')
  const [excludeList, setExcludeList] = useState('')
  const [progress, setProgress] = useState(0)
  // Opt-in: split this backup into parts (for the first backup of a large volume).
  // Off by default → no size analysis, the backup starts immediately.
  const [splitFirstBackup, setSplitFirstBackup] = useState(false)

  // Scheduling states
  const [backupMode, setBackupMode] = useState('oneshot') // 'oneshot' or 'scheduled'
  const [scheduleTime, setScheduleTime] = useState('02:00')
  const [runAtStartup, setRunAtStartup] = useState(false)
  const [scheduledJobs, setScheduledJobs] = useState([])
  const [jobHistory, setJobHistory] = useState([])
  const [editingJobId, setEditingJobId] = useState(null) // Track which job is being edited
  const [backupStats, setBackupStats] = useState({
    startTime: null,
    lastUpdate: null,
    lastPercent: 0,
    speed: 0,
    eta: null,
    // Structured live stats (from the backup:stats event)
    bytesDone: 0,
    bytesTotal: 0,
    newChunks: 0,
    reusedChunks: 0,
    failedChunks: 0,
    currentDir: ''
  })
  const [status, setStatus] = useState({ message: '', type: '', visible: false })

  const [snapshots, setSnapshots] = useState([])
  const [restoreBackupId, setRestoreBackupId] = useState('')
  const [showSnapshots, setShowSnapshots] = useState(false)
  const [restorePBSID, setRestorePBSID] = useState('')
  const [selectedSnapshot, setSelectedSnapshot] = useState(null) // { id, unix, time }
  const [snapshotMeta, setSnapshotMeta] = useState(null)         // .nimbus_backup_meta.json sidecar (null if legacy)
  const [snapshotEntries, setSnapshotEntries] = useState([])     // flat list from backend
  const [imageDisk, setImageDisk] = useState(null)                // volume mode: the .img.fidx being browsed
  const [imageTruncated, setImageTruncated] = useState(false)     // volume tree hit the entry cap
  const [browsingImage, setBrowsingImage] = useState(false)       // image walk in flight (button spinner)
  const [imagePartitions, setImagePartitions] = useState(null)    // partition picker rows (null = not loaded)
  const [imagePartIndex, setImagePartIndex] = useState(0)         // which partition is open (0 = none)
  const [loadingParts, setLoadingParts] = useState(false)
  // In-app path picker (replaces the native dialogs, which crashed the process)
  const [picker, setPicker] = useState(null)  // {mode, initialPath, defaultFileName, needBytes, resolve}
  // Control server (NimbusControl) status + settings form
  const [cpStatus, setCpStatus] = useState(null)
  const [cpForm, setCpForm] = useState({ url: '', token: '', fp: '' })
  const [expandedDirs, setExpandedDirs] = useState(new Set())     // expanded paths in tree
  const [selectedPaths, setSelectedPaths] = useState(new Set())   // selected entry paths
  const [restoreDestPath, setRestoreDestPath] = useState('')
  // 'original' (in-place), 'alternate_abs' (preserve tree), 'alternate_flat' (strip prefix)
  const [restoreMode, setRestoreMode] = useState('alternate_abs')
  const [restoreAllowCrossHost, setRestoreAllowCrossHost] = useState(false)
  // alternate sub-mode toggle: true = abs (keep tree), false = flat. Default flat per spec.
  const [restoreKeepTree, setRestoreKeepTree] = useState(false)
  const [restoreOptions, setRestoreOptions] = useState({
    overwrite: false,
    timestamps: true,
    acls: false, // disabled in UI until NTFS sidecar lands
    ads: false   // disabled in UI until NTFS sidecar lands
  })
  const [restoreLoading, setRestoreLoading] = useState(false)
  const [restoreProgress, setRestoreProgress] = useState(0)

  // ===== file search across snapshots =====
  const [showSearch, setShowSearch] = useState(false)
  const [searchQuery, setSearchQuery] = useState('')
  const [searchMode, setSearchMode] = useState('name')      // 'name' | 'regex' | 'path'
  const [searchFrom, setSearchFrom] = useState('')           // yyyy-mm-dd
  const [searchTo, setSearchTo] = useState('')               // yyyy-mm-dd
  const [searchAssembleMissing, setSearchAssembleMissing] = useState(false)
  const [searchRunning, setSearchRunning] = useState(false)
  const [searchProgress, setSearchProgress] = useState({ percent: 0, message: '' })
  const [searchResult, setSearchResult] = useState(null)     // { hits, snapshots_*, truncated, cancelled }

  // Update restoreBackupId when config or hostname changes
  // Control server status: fetch at mount and every 30 s.
  useEffect(() => {
    if (!GetControlServerStatus) return
    let alive = true
    const load = async () => {
      try {
        const st = await GetControlServerStatus()
        if (!alive) return
        setCpStatus(st)
        setCpForm(f => f.url === '' && st && st.server_host ? { ...f, url: (st.server_host.startsWith('http') ? st.server_host : 'https://' + st.server_host) } : f)
      } catch (e) { /* service unreachable — keep last known */ }
    }
    load()
    const iv = setInterval(load, 30000)
    return () => { alive = false; clearInterval(iv) }
  }, [])

  const saveControlServer = async () => {
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

  useEffect(() => {
    if (GetSecurityWarnings) {
      GetSecurityWarnings().then(w => setSecurityWarnings(w || [])).catch(() => {})
    }
    if (GetExchangeStatus) {
      GetExchangeStatus().then(st => {
        setExchangeStatus(st || {})
        if (st && st.installed && QueryExchangeLogMode) {
          QueryExchangeLogMode().then(m => setExchangeLogMode(m || {})).catch(() => {})
        }
      }).catch(() => {})
    }
  }, [])

  useEffect(() => {
    if (!restoreBackupId && (config['backup-id'] || hostname)) {
      setRestoreBackupId(config['backup-id'] || hostname)
    }
  }, [config['backup-id'], hostname])

  // When the selected snapshot has no sidecar (legacy) or its OS doesn't match
  // the current host, in-place is impossible. Snap the mode back to alternate
  // so a stale "original" selection doesn't survive across snapshot switches.
  useEffect(() => {
    if (restoreMode !== 'original') return
    if (!snapshotMeta) { setRestoreMode('alternate_abs'); return }
    if (!snapshotMeta.original_path) { setRestoreMode('alternate_abs'); return }
    if (systemInfo.os && snapshotMeta.os && systemInfo.os !== snapshotMeta.os) {
      setRestoreMode('alternate_abs')
    }
  }, [snapshotMeta, systemInfo.os, restoreMode])

  // Sync restore PBS dropdown with default once it's loaded
  useEffect(() => {
    if (!restorePBSID && defaultPBSID) {
      setRestorePBSID(defaultPBSID)
    }
  }, [defaultPBSID])

  // Load physical disks when switching to machine (full-volume) mode.
  // Retries on each re-entry to the Machine tab (effect re-runs on backupType).
  useEffect(() => {
    if (backupType !== 'machine' || !ListPhysicalDisks) return
    if (physicalDisks.length > 0 || disksLoading) return
    setDisksError('')
    setDisksLoading(true)
    ListPhysicalDisks().then(disks => {
      setPhysicalDisks(disks || [])
      // Select first disk by default
      if (disks && disks.length > 0 && selectedDrives.length === 0) {
        setSelectedDrives([disks[0].path])
      }
    }).catch(err => {
      setDisksError(String(err))
    }).finally(() => {
      setDisksLoading(false)
    })
  }, [backupType])

  // Listen to backup events
  useEffect(() => {
    if (!EventsOn) return

    const unsubProgress = EventsOn('backup:progress', (data) => {
      const now = Date.now()
      const percent = Math.round(data.percent)
      setProgress(percent)
      showStatus(`🔄 ${data.message}`, 'info')

      // Calculate speed and ETA
      setBackupStats(prev => {
        const startTime = prev.startTime || now
        const lastUpdate = prev.lastUpdate || now
        const timeDiff = (now - lastUpdate) / 1000 // seconds
        const percentDiff = percent - prev.lastPercent

        // Calculate speed (percent per second)
        let speed = prev.speed
        if (timeDiff > 0 && percentDiff > 0) {
          speed = percentDiff / timeDiff
        }

        // Calculate ETA (seconds remaining)
        let eta = null
        if (speed > 0 && percent < 100) {
          const remainingPercent = 100 - percent
          eta = Math.round(remainingPercent / speed)
        }

        return {
          ...prev, // preserve structured stats (bytes/chunks) set by backup:stats
          startTime,
          lastUpdate: now,
          lastPercent: percent,
          speed,
          eta
        }
      })
    })

    // Structured live statistics (bytes + chunk counts) emitted alongside progress.
    const unsubStats = EventsOn('backup:stats', (data) => {
      setBackupStats(prev => ({
        ...prev,
        bytesDone: data.bytesDone || 0,
        bytesTotal: data.bytesTotal || 0,
        newChunks: data.newChunks || 0,
        reusedChunks: data.reusedChunks || 0,
        failedChunks: data.failedChunks || 0,
        currentDir: data.currentDir || ''
      }))
    })

    const unsubComplete = EventsOn('backup:complete', (data) => {
      setProgress(data.success ? 100 : 0)
      setBackupStats({ startTime: null, lastUpdate: null, lastPercent: 0, speed: 0, eta: null, bytesDone: 0, bytesTotal: 0, newChunks: 0, reusedChunks: 0, failedChunks: 0, currentDir: '' })
      showStatus(data.success ? '✅ ' + data.message : '❌ ' + data.message, data.success ? 'success' : 'error')

      // Add to job history
      const historyEntry = {
        id: Date.now().toString(),
        name: `Backup ${config['backup-id'] || hostname}`,
        timestamp: new Date().toISOString(),
        status: data.success ? 'success' : 'failed',
        message: data.message,
        backupDirs: backupDirs.split('\n').map(d => d.trim()).filter(d => d),
        backupId: config['backup-id'] || hostname,
        useVSS: config.usevss
      }
      setJobHistory(prev => [historyEntry, ...prev].slice(0, 20)) // Keep last 20 entries
    })

    return () => {
      if (unsubProgress) unsubProgress()
      if (unsubStats) unsubStats()
      if (unsubComplete) unsubComplete()
    }
  }, [])

  // Listen to restore events
  useEffect(() => {
    if (!EventsOn) return
    const unsubP = EventsOn('restore:progress', (data) => {
      setRestoreProgress(Math.round((data.percent || 0) * 100))
      showStatus(`🔄 ${data.message || ''}`, 'info')
    })
    const unsubC = EventsOn('restore:complete', (data) => {
      setRestoreLoading(false)
      setRestoreProgress(data.success ? 100 : 0)
      showStatus(data.success ? `✅ ${data.message}` : `❌ ${data.message}`, data.success ? 'success' : 'error')
    })
    return () => {
      if (unsubP) unsubP()
      if (unsubC) unsubC()
    }
  }, [])

  // Listen to search progress
  useEffect(() => {
    if (!EventsOn) return
    const unsub = EventsOn('search:progress', (data) => {
      setSearchProgress({ percent: Math.round((data.percent || 0) * 100), message: data.message || '' })
    })
    return () => { if (unsub) unsub() }
  }, [])

  // Split size-analysis progress (explicit-split path only) — folder-by-folder so
  // a multi-minute scan of a large volume shows movement instead of a frozen spinner.
  useEffect(() => {
    if (!EventsOn) return
    const unsub = EventsOn('analysis:progress', (data) => {
      const done = data.done || 0
      const total = data.total || 0
      const gb = ((data.bytes || 0) / (1024 * 1024 * 1024)).toFixed(1)
      showStatus(`📊 ${t('splitAnalyzing')} ${done}/${total} (${gb} GB)`, 'info')
    })
    return () => { if (unsub) unsub() }
  }, [])

  // Load config with hostname on mount
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

  // Load scheduled jobs and history on mount
  useEffect(() => {
    const loadSchedulerData = async () => {
      try {
        if (GetScheduledJobs) {
          const jobs = await GetScheduledJobs()
          setScheduledJobs(jobs || [])
        }

        if (GetJobHistory) {
          const history = await GetJobHistory()
          setJobHistory(history || [])
        }
      } catch (err) {
        console.error('Failed to load scheduler data:', err)
      }
    }

    loadSchedulerData()

    // Refresh history every 10 seconds to update status of running jobs
    const intervalId = setInterval(() => {
      if (GetJobHistory) {
        GetJobHistory().then(history => {
          setJobHistory(history || [])
        }).catch(err => {
          console.error('Failed to refresh job history:', err)
        })
      }
    }, 10000) // 10 seconds

    return () => clearInterval(intervalId)
  }, [])

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
          setSelectedPBSID(defaultID || '')
        }
      } catch (err) {
        console.error('Failed to load PBS servers:', err)
      }
    }

    loadPBSServers()
  }, [])

  // localizeMessage maps backend error codes ([NB-xxxx]) to the active
  // language. Contract with the Go side (gui/errcodes.go): the coded English
  // base may carry dynamic detail after " :: ", which is preserved verbatim.
  const localizeMessage = (msg) => {
    if (!msg) return msg
    const raw = String(msg)
    const m = raw.match(/\[NB-(\d{4})\]/)
    if (!m) return raw
    const key = 'err_NB' + m[1]
    const tr = t(key)
    if (tr === key) return raw // no translation available: show as-is
    const prefix = raw.slice(0, raw.indexOf(m[0]))
    const rest = raw.slice(raw.indexOf(m[0]) + m[0].length)
    const di = rest.indexOf(' :: ')
    const detail = di >= 0 ? rest.slice(di + 4) : ''
    return prefix + '[NB-' + m[1] + '] ' + tr + (detail ? ': ' + detail : '')
  }

  const showStatus = (message, type) => {
    setStatus({ message: localizeMessage(message), type, visible: true })
    setTimeout(() => {
      setStatus(s => ({ ...s, visible: false }))
    }, 5000)
  }

  // ==================== MULTI-PBS HANDLERS ====================

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
      showStatus(`❌ ${t('statusServerLoadError')} ${err}`, 'error')
    }
  }

  const handleAddPBSServer = async () => {
    if (!AddPBSServer) {
      showStatus('❌ Wails runtime non disponible', 'error')
      return
    }

    try {
      // Generate ID from name if not provided
      if (!serverFormData.id) {
        serverFormData.id = serverFormData.name.toLowerCase().replace(/[^a-z0-9]/g, '-')
      }

      await AddPBSServer(serverFormData)
      showStatus(`✅ ${t('statusServerAdded')}`, 'success')

      // Reset form and reload
      setServerFormData({
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
      setEditingServer(null)
      await loadPBSServers()
    } catch (err) {
      showStatus(`❌ Erreur: ${err}`, 'error')
    }
  }

  const handleUpdatePBSServer = async () => {
    if (!UpdatePBSServer) {
      showStatus('❌ Wails runtime non disponible', 'error')
      return
    }

    try {
      await UpdatePBSServer(serverFormData)
      showStatus(`✅ ${t('statusServerUpdated')}`, 'success')

      // Reset form and reload
      setServerFormData({
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
      setEditingServer(null)
      await loadPBSServers()
    } catch (err) {
      showStatus(`❌ Erreur: ${err}`, 'error')
    }
  }

  const handleDeletePBSServer = async (id) => {
    if (!DeletePBSServer) {
      showStatus('❌ Wails runtime non disponible', 'error')
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
      showStatus(`❌ Erreur: ${err}`, 'error')
    }
  }

  const handleSetDefaultPBS = async (id) => {
    if (!SetDefaultPBSServer) {
      showStatus('❌ Wails runtime non disponible', 'error')
      return
    }

    try {
      await SetDefaultPBSServer(id)
      setDefaultPBSID(id)
      showStatus(`✅ ${t('statusServerSetDefault').replace('{id}', id)}`, 'success')
    } catch (err) {
      showStatus(`❌ Erreur: ${err}`, 'error')
    }
  }

  // True when a connection failure is an unverified-certificate error (self-signed
  // PBS with no fingerprint pinned). These are recoverable via trust-on-first-use.
  const isCertError = (err) =>
    /certificate signed by unknown authority|failed to verify certificate|x509/i.test(String(err))

  const handleTestPBSConnection = async (id) => {
    if (!TestPBSConnection) {
      showStatus('❌ Wails runtime non disponible', 'error')
      return
    }

    try {
      setServerStatus(prev => ({ ...prev, [id]: 'testing' }))
      await TestPBSConnection(id)
      setServerStatus(prev => ({ ...prev, [id]: 'online' }))
      showStatus(`✅ ${t('statusConnectionSuccess').replace('{id}', id)}`, 'success')
    } catch (err) {
      const server = pbsServers.find(s => s.id === id)
      // Self-signed cert + no fingerprint yet: offer to discover and pin it (TOFU).
      if (isCertError(err) && server && !(server.certfingerprint || '').trim() &&
          GetServerFingerprint && PinPBSServerFingerprint) {
        try {
          const fp = await GetServerFingerprint(server.baseurl)
          if (window.confirm(t('tofuConfirm').replace('{host}', server.baseurl).replace('{fp}', fp))) {
            await PinPBSServerFingerprint(id, fp)
            await loadPBSServers()
            await TestPBSConnection(id)
            setServerStatus(prev => ({ ...prev, [id]: 'online' }))
            showStatus(`✅ ${t('statusFingerprintPinned')}`, 'success')
            return
          }
        } catch (fpErr) {
          showStatus(`❌ ${t('statusFingerprintFailed')} ${fpErr}`, 'error')
          setServerStatus(prev => ({ ...prev, [id]: 'offline' }))
          return
        }
      }
      setServerStatus(prev => ({ ...prev, [id]: 'offline' }))
      showStatus(`❌ ${t('statusConnectionFailed')} ${err}`, 'error')
    }
  }

  const handleEditServer = (server) => {
    setServerFormData(server)
    setEditingServer(server.id)
  }

  const handleCancelEdit = () => {
    setServerFormData({
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
    setEditingServer(null)
  }

  // ==================== END MULTI-PBS HANDLERS ====================

  const handleSaveConfig = async () => {
    if (!SaveConfig) {
      showStatus('❌ Wails runtime non disponible', 'error')
      return
    }

    try {
      // Trim all string values to remove whitespace (with safe fallback for undefined)
      const trimmedConfig = {
        baseurl: (config.baseurl || '').trim(),
        certfingerprint: (config.certfingerprint || '').trim(),
        authid: (config.authid || '').trim(),
        secret: (config.secret || '').trim(),
        datastore: (config.datastore || '').trim(),
        namespace: (config.namespace || '').trim(),
        backupdir: (config.backupdir || '').trim(),
        'backup-id': (config['backup-id'] || '').trim() || hostname, // Use hostname if empty
        usevss: config.usevss !== undefined ? config.usevss : true,
        upload_limit_mbps: Number(config.upload_limit_mbps) || 0,
        exchange_aware: !!config.exchange_aware,
        exchange_log_truncation: !!config.exchange_log_truncation
      }
      await SaveConfig(trimmedConfig)
      setConfig(trimmedConfig)
      showStatus(`✅ ${t('statusConfigSaved')}`, 'success')
    } catch (err) {
      showStatus(`❌ Erreur : ${err}`, 'error')
    }
  }

  const handleTestConnection = async () => {
    if (!TestConnection) {
      showStatus('❌ Wails runtime non disponible', 'error')
      return
    }

    // Test with current form values (no need to save first). Declared outside the
    // try so the TOFU retest in catch can reuse these already-normalized fields.
    const testConfig = {
      baseurl: (config.baseurl || '').trim(),
      certfingerprint: (config.certfingerprint || '').trim(),
      authid: (config.authid || '').trim(),
      secret: (config.secret || '').trim(),
      datastore: (config.datastore || '').trim(),
      namespace: (config.namespace || '').trim(),
      backupdir: (config.backupdir || '').trim(),
      'backup-id': (config['backup-id'] || '').trim() || hostname, // Use hostname if empty
      usevss: config.usevss !== undefined ? config.usevss : true
    }

    try {
      await TestConnection(testConfig)
      showStatus(`✅ ${t('statusConnectionOK')}`, 'success')
    } catch (err) {
      // Self-signed cert + no fingerprint yet: discover, re-test pinned, fill the
      // form so a Save persists it (legacy single-server config).
      if (isCertError(err) && !(config.certfingerprint || '').trim() && GetServerFingerprint) {
        try {
          const fp = await GetServerFingerprint((config.baseurl || '').trim())
          if (window.confirm(t('tofuConfirm').replace('{host}', config.baseurl).replace('{fp}', fp))) {
            // Reuse the already-normalized testConfig (trimmed fields) for the pinned retest.
            await TestConnection({ ...testConfig, certfingerprint: fp })
            setConfig(prev => ({ ...prev, certfingerprint: fp }))
            showStatus(`✅ ${t('statusFingerprintPinnedSave')}`, 'success')
            return
          }
        } catch (fpErr) {
          showStatus(`❌ ${t('statusFingerprintFailed')} ${fpErr}`, 'error')
          return
        }
      }
      showStatus(`❌ ${err}`, 'error')
    }
  }

  const handleLoadConfigFile = (e) => {
    const file = e.target.files[0]
    if (!file) return

    const reader = new FileReader()
    reader.onload = (evt) => {
      try {
        const loadedConfig = JSON.parse(evt.target.result)
        setConfig(loadedConfig)
        showStatus(`✅ ${t('statusConfigLoaded')}`, 'success')
      } catch (err) {
        showStatus(`❌ ${t('statusInvalidJSON')}`, 'error')
      }
    }
    reader.readAsText(file)
  }

  // Execute split backup for large volumes (explicit opt-in via the split toggle).
  // CreateBackupSplitPlan runs the size analysis the user asked for (bounded, and
  // reporting folder-by-folder progress via the "analysis:progress" event) and
  // returns one job per part; we then run a backup per part.
  const executeSplitBackup = async (dirList) => {
    if (!window.go || !window.go.main.App.CreateBackupSplitPlan) {
      showStatus('❌ Split backup not available', 'error')
      return
    }

    try {
      showStatus(`📊 ${t('splitAnalyzing')}`, 'info')
      const splitPlan = await window.go.main.App.CreateBackupSplitPlan(
        dirList,
        config['backup-id'] || hostname
      )

      // If the analysis didn't yield a real split (volume below threshold, scan
      // incomplete, or splitting disabled), the single "job" only covers the
      // enumerated subfolders and would DROP files sitting directly under the
      // selected root. Fall back to a normal full backup of the roots, which
      // always captures everything.
      if (!splitPlan || splitPlan.length <= 1) {
        showStatus(`🚀 ${t('statusBackupStarting')}`, 'info')
        setProgress(5)
        await StartBackup(
          backupType,
          dirList,
          selectedDrives,
          excludeList.split('\n').filter(l => l.trim()),
          config['backup-id'],
          config.usevss,
          ''
        )
        showStatus(`⏳ ${t('statusBackupRunning')}`, 'info')
        return
      }

      showStatus(`🔄 Lancement de ${splitPlan.length} backups partiels...`, 'info')

      // Execute split jobs sequentially
      for (let i = 0; i < splitPlan.length; i++) {
        const job = splitPlan[i]
        showStatus(
          `📦 Backup ${job.index}/${job.total_jobs}: ${job.size_fmt}...`,
          'info'
        )

        try {
          await StartBackup(
            backupType,
            job.folders,
            selectedDrives,
            // Merge user exclusions with this job's own (a root-remainder job
            // excludes the subfolders already covered by other jobs — v2-H-01).
            [...excludeList.split('\n').filter(l => l.trim()), ...(job.exclude_list || [])],
            job.backup_id,
            config.usevss,
            ''
          )

          // Wait for completion (simplified - in production, use event polling)
          showStatus(
            `✅ Backup ${job.index}/${job.total_jobs} terminé`,
            'success'
          )
        } catch (err) {
          showStatus(
            `❌ Backup ${job.index}/${job.total_jobs} échoué: ${err}`,
            'error'
          )

          const retry = window.confirm(
            `Le backup ${job.index}/${job.total_jobs} a échoué.\n\n` +
            `Voulez-vous réessayer ce backup avant de continuer?`
          )

          if (retry) {
            i-- // Retry same job
          } else {
            throw new Error(`Split backup ${job.index} failed`)
          }
        }
      }

      showStatus(
        `🎉 Tous les backups partiels terminés avec succès (${splitPlan.length}/${splitPlan.length})`,
        'success'
      )
    } catch (err) {
      showStatus(`❌ Erreur split backup: ${err}`, 'error')
    }
  }

  const handleStartBackup = async () => {
    if (!StartBackup) {
      showStatus('❌ Wails runtime non disponible', 'error')
      return
    }

    // Parse backup directories (one per line)
    const dirList = backupDirs.split('\n').map(d => d.trim()).filter(d => d)

    if (backupType === 'directory' && dirList.length === 0) {
      showStatus('❌ Au moins un répertoire requis', 'error')
      return
    }

    if (backupType === 'machine' && selectedDrives.length === 0) {
      showStatus('❌ Au moins un disque requis', 'error')
      return
    }

    // Splitting is now an explicit, opt-in choice (the "split this backup" toggle),
    // intended for the first backup of a large volume. When it's off we never size
    // the directories — the backup starts immediately, so a whole-drive root like
    // C:\ can no longer hang on the analysis. When it's on, executeSplitBackup runs
    // the (bounded, progress-reporting) size analysis the user asked for, then runs
    // one backup per part. Subsequent backups are left unsplit (full).
    if (splitFirstBackup && backupType === 'directory' && backupMode === 'oneshot') {
      await executeSplitBackup(dirList)
      return
    }

    // Scheduled mode - save or update job instead of executing immediately
    if (backupMode === 'scheduled') {
      if (!SaveScheduledJob || !UpdateScheduledJob) {
        showStatus('❌ Fonction de planification non disponible', 'error')
        return
      }

      const jobData = {
        id: editingJobId || Date.now().toString(),
        name: `Backup ${config['backup-id'] || hostname}`,
        scheduleTime: scheduleTime,
        runAtStartup: runAtStartup,
        backupDirs: dirList,
        driveLetters: selectedDrives,
        backupId: config['backup-id'],
        useVSS: config.usevss,
        backupType: backupType,
        excludeList: excludeList.split('\n').filter(l => l.trim())
      }

      // Save or update to backend
      try {
        if (editingJobId) {
          // Update existing job
          await UpdateScheduledJob(jobData)
          setScheduledJobs(scheduledJobs.map(j => j.id === editingJobId ? jobData : j))
          showStatus(`✅ Backup modifié pour ${scheduleTime}`, 'success')
          setEditingJobId(null)
        } else {
          // Create new job
          await SaveScheduledJob(jobData)
          setScheduledJobs([...scheduledJobs, jobData])
          showStatus(`✅ Backup planifié pour ${scheduleTime}`, 'success')
        }
        // Reset form after save
        setScheduleTime('02:00')
        setRunAtStartup(false)
        setBackupDirs('')
      } catch (err) {
        showStatus(`❌ Erreur: ${err}`, 'error')
      }
      return
    }

    // One-shot mode - execute immediately
    showStatus(`🚀 ${t('statusBackupStarting')}`, 'info')
    setProgress(5)

    try {
      await StartBackup(
        backupType,
        dirList,
        selectedDrives,
        excludeList.split('\n').filter(l => l.trim()),
        config['backup-id'],
        config.usevss,
        ''
      )
      // Backup started in background - progress will be shown via events
      showStatus(`⏳ ${t('statusBackupRunning')}`, 'info')
    } catch (err) {
      setProgress(0)
      showStatus(`❌ ${err}`, 'error')
    }
  }

  const handleListSnapshots = async () => {
    if (!ListSnapshots) {
      showStatus('❌ Wails runtime non disponible', 'error')
      return
    }
    if (!restoreBackupId) {
      showStatus('❌ Backup ID requis', 'error')
      return
    }

    showStatus('🔍 Recherche des snapshots...', 'info')
    setSelectedSnapshot(null)
    setSnapshotEntries([])
    setSelectedPaths(new Set())
    setExpandedDirs(new Set())

    try {
      const snaps = await ListSnapshots(restorePBSID || '', restoreBackupId)
      setSnapshots(snaps || [])
      setShowSnapshots(true)
      showStatus(`✅ ${snaps.length} snapshot(s) trouvé(s)`, 'success')
    } catch (err) {
      showStatus(`❌ ${err}`, 'error')
    }
  }

  const handleSelectSnapshot = async (snap, forceRefresh = false) => {
    if (!ListSnapshotContents) {
      showStatus('❌ Wails runtime non disponible', 'error')
      return
    }
    setSelectedSnapshot(snap)
    setSnapshotMeta(null)
    setSnapshotEntries([])
    setSelectedPaths(new Set())
    setExpandedDirs(new Set())
    setImageDisk(null)
    setImageTruncated(false)
    // Volume (disk image) snapshots have no pxar catalog to list — the panel
    // shows the disks with a Browse-files button per disk instead.
    if (isVolumeSnapshot(snap)) {
      showStatus(`💿 ${t('volumeSnapSelected')}`, 'info')
      return
    }
    showStatus(`📥 ${t('loadingSnapshotContents')}`, 'info')
    const effectiveBackupId = snap.backup_id || restoreBackupId
    try {
      // Backend uses the snapshot's actual backup_id (snap.backup_id) so split
      // backups list their real contents, not the partial search term.
      const entries = await ListSnapshotContents(restorePBSID || '', effectiveBackupId, snap.unix, forceRefresh)
      setSnapshotEntries(entries || [])
      showStatus(`✅ ${(entries || []).length} ${t('entriesLoaded')}`, 'success')
    } catch (err) {
      showStatus(`❌ ${err}`, 'error')
    }
    // Meta is informational — fire-and-forget. The listing call above has
    // already populated the cache, so this is a cheap cache hit. Failure is
    // silent: legacy snapshots simply have no sidecar.
    if (GetSnapshotMeta) {
      try {
        const meta = await GetSnapshotMeta(restorePBSID || '', effectiveBackupId, snap.unix)
        if (meta) setSnapshotMeta(meta)
      } catch (_err) {
        // ignored — banner stays hidden
      }
    }
  }

  const handleReloadSnapshot = async () => {
    if (!selectedSnapshot) return
    await handleSelectSnapshot(selectedSnapshot, true)
  }

  // The in-app picker, as a promise — call sites read like the old dialog API
  // but nothing native is involved, so nothing can fault.
  const pickerApi = { ListDrives, ListFolders, CreateFolder, DefaultSaveDir }
  const openPicker = (opts) => new Promise((resolve) => {
    setPicker({ ...opts, resolve })
  })
  const closePicker = (value) => {
    setPicker(p => { if (p && p.resolve) p.resolve(value); return null })
  }

  const handleBrowseRestoreDest = async () => {
    const dir = await openPicker({
      mode: 'folder',
      initialPath: restoreDestPath || '',
      needBytes: selectionBytes || 0,
    })
    if (dir) setRestoreDestPath(dir)
  }

  // ===== file search handlers =====

  // Convert a yyyy-mm-dd local date string to Unix seconds. endOfDay pushes to
  // 23:59:59 so the "To" bound is inclusive of the whole day. "" → 0 (open).
  const parseDateToUnix = (s, endOfDay = false) => {
    if (!s) return 0
    const [y, m, d] = s.split('-').map(Number)
    if (!y || !m || !d) return 0
    const dt = endOfDay ? new Date(y, m - 1, d, 23, 59, 59) : new Date(y, m - 1, d, 0, 0, 0)
    return Math.floor(dt.getTime() / 1000)
  }

  const handleSearch = async () => {
    if (!SearchFiles) {
      showStatus('❌ Wails runtime non disponible', 'error')
      return
    }
    if (!searchQuery.trim()) {
      showStatus('❌ ' + t('searchQueryRequired'), 'error')
      return
    }
    const prefix = (restoreBackupId || hostname || '').trim()
    setSearchRunning(true)
    setSearchResult(null)
    setSearchProgress({ percent: 0, message: '' })
    try {
      const res = await SearchFiles(
        restorePBSID || '',
        prefix,
        searchQuery,
        searchMode,
        parseDateToUnix(searchFrom, false),
        parseDateToUnix(searchTo, true),
        searchAssembleMissing
      )
      setSearchResult(res)
      const n = (res && res.hits) ? res.hits.length : 0
      showStatus(`✅ ${t('searchDone').replace('{n}', n)}`, 'success')
    } catch (err) {
      showStatus(`❌ ${err}`, 'error')
    } finally {
      setSearchRunning(false)
    }
  }

  const handleCancelSearch = () => {
    if (CancelSearch) {
      try { CancelSearch() } catch (_e) { /* best effort */ }
    }
  }

  // Pre-fill the restore form from a search hit: select the hit's snapshot,
  // load its contents, and tick the matched entry.
  const handleRestoreHit = async (hit) => {
    setRestoreBackupId(hit.backup_id)
    const iso = new Date(hit.snapshot_time * 1000).toISOString()
    const snap = {
      id: iso.slice(0, 19) + 'Z',
      backup_id: hit.backup_id,
      unix: hit.snapshot_time,
      time: new Date(hit.snapshot_time * 1000).toLocaleString(),
    }
    setShowSnapshots(true)
    await handleSelectSnapshot(snap)
    setSelectedPaths(new Set([hit.path]))
    const parts = hit.path.split('/')
    const dirs = new Set()
    let acc = ''
    for (let i = 0; i < parts.length - 1; i++) {
      acc = acc ? `${acc}/${parts[i]}` : parts[i]
      dirs.add(acc)
    }
    setExpandedDirs(dirs)
  }

  // inPlaceBlocker returns a translation key (or null when in-place is OK).
  // Drives the disabled state of the in-place radio + its tooltip.
  const inPlaceBlocker = () => {
    if (!snapshotMeta) return 'inPlaceNoMeta'
    if (!snapshotMeta.original_path) return 'inPlaceNoOriginalPath'
    if (systemInfo.os && snapshotMeta.os && systemInfo.os !== snapshotMeta.os) return 'inPlaceOsMismatch'
    return null
  }

  // crossHostMismatch returns true when the backup hostname differs from the
  // current machine. Comparison is case-insensitive and ignores the domain
  // suffix — same rule as backend equalHostnames.
  const crossHostMismatch = () => {
    if (!snapshotMeta || !snapshotMeta.hostname || !hostname) return false
    const norm = s => (s || '').toLowerCase().split('.')[0]
    return norm(snapshotMeta.hostname) !== norm(hostname)
  }

  const handleRestoreSnapshot = async () => {
    if (!RestoreSnapshot) {
      showStatus('❌ Wails runtime non disponible', 'error')
      return
    }
    if (!selectedSnapshot) {
      showStatus('❌ ' + t('selectSnapshotFirst'), 'error')
      return
    }

    // Resolve effective mode. The UI radio is binary (in-place / alternate);
    // the alternate sub-mode comes from the "keep tree" toggle.
    let effectiveMode = restoreMode
    if (restoreMode !== 'original') {
      effectiveMode = restoreKeepTree ? 'alternate_abs' : 'alternate_flat'
    }

    if (effectiveMode !== 'original' && !restoreDestPath) {
      showStatus('❌ ' + t('destinationRequired'), 'error')
      return
    }

    // In-place: scary, get explicit confirmation. confirm() is a stopgap until
    // we wire a real modal — for the alpha phase it's enough and the message
    // is precise about what will happen.
    if (effectiveMode === 'original') {
      const target = snapshotMeta?.original_path || '?'
      const msg = t('inPlaceConfirm').replace('{path}', target)
      // eslint-disable-next-line no-alert
      if (!window.confirm(msg)) {
        return
      }
    }

    // Empty selection = restore everything in the snapshot. For an image
    // backup that would mean a full-image restore, which is NOT what this
    // button does — require an explicit selection instead of silently doing
    // the wrong (and very large) thing.
    const includes = Array.from(selectedPaths)
    if (imageDisk && includes.length === 0) {
      showStatus('❌ ' + t('imageRestoreNeedsSelection'), 'error')
      return
    }

    setRestoreLoading(true)
    setRestoreProgress(0)
    showStatus(`🔄 ${t('statusRestoring').replace('{time}', selectedSnapshot.time)}`, 'info')

    try {
      if (imageDisk) {
        // Volume backup: restore the selected files OUT of the disk image.
        // The pxar path cannot serve this — a vm snapshot has no
        // backup.pxar.didx, which is why the old code 400'd at PBS.
        await RestoreImageSelection(
          restorePBSID || '',
          selectedSnapshot.backup_id || restoreBackupId,
          selectedSnapshot.id,
          selectedSnapshot.backup_type || 'vm',
          imageDisk,
          imagePartIndex,
          includes,
          restoreDestPath,
          restoreKeepTree,
          restoreOptions.overwrite,
          selectionBytes || 0
        )
        setRestoreLoading(false)
        showStatus('✅ ' + t('restoreDone'), 'success')
        return
      }
      await RestoreSnapshot(
        restorePBSID || '',
        selectedSnapshot.backup_id || restoreBackupId,
        selectedSnapshot.id,
        restoreDestPath,
        effectiveMode,
        includes,
        restoreAllowCrossHost,
        restoreOptions.acls,
        restoreOptions.ads,
        restoreOptions.timestamps,
        restoreOptions.overwrite
      )
      // Completion arrives via the restore:complete event.
    } catch (err) {
      setRestoreLoading(false)
      showStatus(`❌ ${err}`, 'error')
    }
  }

  // ===== tree helpers (snapshot navigation) =====

  // Build a map childrenByDir: dir -> [entry...] from the flat list, plus a
  // set of all dir paths. Re-derived on every render — entries are tiny.
  const buildTree = (entries) => {
    const childrenByDir = new Map()
    const dirSet = new Set([''])
    childrenByDir.set('', [])
    for (const e of entries) {
      if (e.is_dir) dirSet.add(e.path)
    }
    for (const e of entries) {
      const slash = e.path.lastIndexOf('/')
      const parent = slash < 0 ? '' : e.path.substring(0, slash)
      // Some archives may emit a child without ever emitting the parent dir
      // entry. Make sure such parents still exist as buckets.
      if (!childrenByDir.has(parent)) childrenByDir.set(parent, [])
      childrenByDir.get(parent).push(e)
      if (e.is_dir && !childrenByDir.has(e.path)) childrenByDir.set(e.path, [])
    }
    // Sort each bucket: dirs first, then alphabetical
    for (const list of childrenByDir.values()) {
      list.sort((a, b) => {
        if (a.is_dir !== b.is_dir) return a.is_dir ? -1 : 1
        return a.path.localeCompare(b.path)
      })
    }
    return childrenByDir
  }

  const toggleDir = (path) => {
    setExpandedDirs(prev => {
      const next = new Set(prev)
      if (next.has(path)) next.delete(path)
      else next.add(path)
      return next
    })
  }

  const togglePathSelection = (path) => {
    setSelectedPaths(prev => {
      const next = new Set(prev)
      if (next.has(path)) next.delete(path)
      else next.add(path)
      return next
    })
  }

  const formatBytes = (bytes) => {
    if (!bytes || bytes < 1024) return `${bytes || 0} B`
    const units = ['KB', 'MB', 'GB', 'TB']
    let n = bytes / 1024
    let i = 0
    while (n >= 1024 && i < units.length - 1) { n /= 1024; i++ }
    return `${n.toFixed(1)} ${units[i]}`
  }

  // Reconstruct the absolute on-disk location a file came from, by joining the
  // backup's original_path (from the meta sidecar) with the archive-relative
  // path. Critical for split backups, where each part is a separate backup-id
  // and the relative tree alone doesn't tell you which physical folder/drive a
  // file belonged to. Returns null when the snapshot has no meta sidecar.
  const absOriginPath = (archivePath) => {
    const root = snapshotMeta?.original_path
    if (!root) return null
    const sep = (snapshotMeta?.os === 'windows' || root.includes('\\')) ? '\\' : '/'
    const base = root.endsWith(sep) ? root.slice(0, -1) : root
    if (!archivePath) return base
    return base + sep + archivePath.split('/').join(sep)
  }

  // Total bytes the current selection will restore. Selecting a directory pulls
  // in all its descendants, so we sum every file that is itself selected or
  // lives under a selected path. An empty selection means "restore everything",
  // so we sum the whole snapshot. Memoized — snapshots can hold 100k+ entries.
  const selectionBytes = useMemo(() => {
    if (!snapshotEntries.length) return 0
    if (selectedPaths.size === 0) {
      return snapshotEntries.reduce((sum, e) => e.is_dir ? sum : sum + (e.size || 0), 0)
    }
    const sel = Array.from(selectedPaths)
    let sum = 0
    for (const e of snapshotEntries) {
      if (e.is_dir) continue
      for (const p of sel) {
        if (e.path === p || e.path.startsWith(p + '/')) { sum += (e.size || 0); break }
      }
    }
    return sum
  }, [snapshotEntries, selectedPaths])

  // Recursive renderer driven by the children map. Depth is only used for the
  // visual indent.
  // Flatten the visible tree (respecting expanded state) in display order —
  // the range that shift+click selects, exactly like a file manager.
  const visibleOrder = () => {
    const tree = buildTree(snapshotEntries)
    const out = []
    const walk = (list) => {
      for (const e of (list || [])) {
        out.push(e.path)
        if (e.is_dir && expandedDirs.has(e.path)) walk(tree.get(e.path))
      }
    }
    walk(tree.get('') || [])
    return out
  }

  // Row click semantics: ctrl(⌘)+click toggles the row; shift+click selects
  // the contiguous visible range from the last clicked row; plain click on a
  // dir expands/collapses (files: toggles the checkbox).
  // Download the current selection. One selected FILE downloads directly
  // under its own name; anything else (folder, or several items) becomes a
  // zip. Pre-flight space check: BLOCK if it cannot fit on the destination
  // drive, WARN (confirm dialog) if it would push the drive to >= 90% used.
  // The Go side re-enforces both rules authoritatively.
  // Volume mode: load the NTFS file tree of one disk image. Partition 0 =
  // first NTFS partition (the Go side errors clearly for BitLocker or
  // non-NTFS). The result feeds the SAME tree UI as directory backups.
  // Step 1: list the disk's partitions. We do NOT auto-open one — picking for
  // the user dropped them inside the WinRE recovery volume, which looked like
  // a bug because it was one.
  const handleListPartitions = async (disk) => {
    if (!ListImagePartitions || !selectedSnapshot) return
    setLoadingParts(true)
    setImagePartitions(null)
    setImagePartIndex(0)
    setSnapshotEntries([])
    setSelectedPaths(new Set())
    setImageDisk(null)
    showStatus(`💿 ${t('volumeReadingParts')}`, 'info')
    try {
      const parts = await ListImagePartitions(
        restorePBSID || '',
        selectedSnapshot.backup_id || restoreBackupId,
        selectedSnapshot.id,
        selectedSnapshot.backup_type || 'vm',
        disk
      )
      setImagePartitions({ disk, rows: parts || [] })
      showStatus(`✅ ${(parts || []).length} ${t('volumePartsFound')}`, 'success')
    } catch (err) {
      const msg = '❌ ' + (err && err.message ? err.message : String(err))
      showStatus(msg, 'error')
      try { window.alert(msg) } catch (e) { /* no-op */ }
    } finally {
      setLoadingParts(false)
    }
  }

  // Step 2: open the partition the user chose.
  const handleBrowseImageDisk = async (disk, partIndex, forceRefresh = false) => {
    if (!selectedSnapshot) {
      showStatus('❌ No snapshot selected', 'error')
      return
    }
    if (!ListImageContents) {
      // The Go binding isn't present — surface it unmistakably rather than
      // silently doing nothing (this is what "the button does nothing" looked
      // like: an early return with no feedback).
      const msg = '❌ ' + (t('volumeBindingMissing') || 'Image browsing is unavailable in this build (backend method not found).')
      showStatus(msg, 'error')
      try { window.alert(msg) } catch (e) { /* no-op */ }
      return
    }
    setSnapshotEntries([])
    setSelectedPaths(new Set())
    setExpandedDirs(new Set())
    setImageDisk(null)
    setImageTruncated(false)
    setBrowsingImage(true)
    showStatus(`💿 ${t('volumeReadingTree')}`, 'info')
    try {
      const res = await ListImageContents(
        restorePBSID || '',
        selectedSnapshot.backup_id || restoreBackupId,
        selectedSnapshot.id,
        selectedSnapshot.backup_type || 'vm',
        disk,
        partIndex,
        forceRefresh
      )
      const entries = Array.isArray(res) ? res : ((res && res.entries) || [])
      setSnapshotEntries(entries)
      setImageDisk(disk)
      setImagePartIndex(partIndex)
      let truncated = false
      if (LastImageListTruncated) { try { truncated = await LastImageListTruncated() } catch (e) { /* ignore */ } }
      setImageTruncated(truncated)
      if (entries.length === 0) {
        // Succeeded but empty — say so explicitly instead of snapping back to
        // the disk list, which reads as "nothing happened".
        showStatus('⚠️ ' + (t('volumeEmptyTree') || 'No files found on this partition.'), 'info')
      } else {
        showStatus(`✅ ${entries.length} ${t('entriesLoaded')}`, 'success')
      }
    } catch (err) {
      const msg = '❌ ' + (err && err.message ? err.message : String(err))
      showStatus(msg, 'error')
      try { window.alert(msg) } catch (e) { /* no-op */ }
    } finally {
      setBrowsingImage(false)
    }
  }

  const handleDownloadSelection = async () => {
    if (!DownloadSelection || selectedPaths.size === 0) return
    const paths = Array.from(selectedPaths)
    // Single regular file? (must exist in entries and not be a dir)
    const single = paths.length === 1 ? snapshotEntries.find(e => e.path === paths[0] && !e.is_dir) : null
    const defaultName = single
      ? (single.path.split('/').pop() || 'download')
      : `${selectedSnapshot.backup_id}-${(selectedSnapshot.time || '').replace(/[: ]/g, '-')}.zip`
    const dest = await openPicker({
      mode: 'save',
      initialPath: restoreDestPath || '',
      defaultFileName: defaultName,
      needBytes: selectionBytes || 0,
    })
    if (!dest) return // user cancelled

    // Pre-flight space math (advisory UX; Go re-checks and enforces).
    try {
      const sc = await CheckDownloadSpace(dest, selectionBytes || 0)
      if (!sc.fits) {
        showStatus('❌ ' + t('downloadBlockedSpace')
          .replace('{need}', formatBytes(sc.needed_bytes))
          .replace('{free}', formatBytes(sc.free_bytes)), 'error')
        return
      }
      if (sc.warn_90) {
        const msg = t('downloadWarn90')
          .replace('{pct}', sc.usage_after_pct.toFixed(1))
          .replace('{free}', formatBytes(sc.free_bytes - sc.needed_bytes))
        if (!window.confirm(msg)) return
      }
    } catch (e) { /* space check unavailable — Go side still enforces */ }

    setDownloading(true)
    try {
      if (imageDisk) {
        await DownloadImageSelection(
          restorePBSID || '',
          selectedSnapshot.backup_id || restoreBackupId,
          selectedSnapshot.id,
          selectedSnapshot.backup_type || 'vm',
          imageDisk,
          imagePartIndex,
          paths,
          dest,
          !single,
          selectionBytes || 0
        )
      } else {
        await DownloadSelection(
          restorePBSID || '',
          selectedSnapshot.backup_id || restoreBackupId,
          selectedSnapshot.id,
          paths,
          dest,
          !single,
          selectionBytes || 0
        )
      }
      showStatus('✅ ' + t('downloadDone').replace('{dest}', dest), 'success')
    } catch (e) {
      showStatus('❌ ' + (e && e.message ? e.message : String(e)), 'error')
    } finally {
      setDownloading(false)
    }
  }

  const handleRowClick = (e, entry) => {
    if (e.shiftKey && lastClickedPath) {
      e.preventDefault()
      const order = visibleOrder()
      const a = order.indexOf(lastClickedPath)
      const b = order.indexOf(entry.path)
      if (a !== -1 && b !== -1) {
        const [lo, hi] = a < b ? [a, b] : [b, a]
        setSelectedPaths(prev => {
          const next = new Set(prev)
          for (let i = lo; i <= hi; i++) next.add(order[i])
          return next
        })
        return
      }
    }
    if (e.ctrlKey || e.metaKey) {
      togglePathSelection(entry.path)
      setLastClickedPath(entry.path)
      return
    }
    setLastClickedPath(entry.path)
    if (entry.is_dir) toggleDir(entry.path)
    else togglePathSelection(entry.path)
  }

  // Folder size = sum of every file beneath it. Computed once per entry list
  // (a Map keyed by dir path) so the tree can show a size column for folders
  // as well as files, for both backup types.
  const dirSizes = useMemo(() => {
    const m = new Map()
    for (const e of snapshotEntries) {
      if (e.is_dir) continue
      let d = e.path.substring(0, e.path.lastIndexOf('/'))
      while (true) {
        const key = d === '' ? '/' : d
        m.set(key, (m.get(key) || 0) + (e.size || 0))
        if (d === '') break
        d = d.substring(0, d.lastIndexOf('/'))
      }
    }
    return m
  }, [snapshotEntries])

  const entrySize = (entry) => entry.is_dir ? (dirSizes.get(entry.path) || 0) : (entry.size || 0)

  const renderTreeNode = (entry, childrenByDir, depth) => {
    const isExpanded = expandedDirs.has(entry.path)
    const isSelected = selectedPaths.has(entry.path)
    const indent = { paddingLeft: `${depth * 16}px` }
    const origin = absOriginPath(entry.path)
    const rowTitle = origin ? t('originTooltip').replace('{path}', origin) : entry.path
    return (
      <div key={entry.path}>
        <div
          title={rowTitle}
          onClick={(e) => handleRowClick(e, entry)}
          style={{ ...indent, display: 'flex', alignItems: 'center', padding: '4px 8px', cursor: 'pointer', borderBottom: '1px solid var(--nc-border-soft)', background: isSelected ? 'var(--nc-row-selected)' : undefined }}>
          <input
            type="checkbox"
            checked={isSelected}
            onClick={(e) => e.stopPropagation()}
            onChange={() => { togglePathSelection(entry.path); setLastClickedPath(entry.path) }}
            style={{ marginRight: '8px' }}
          />
          {/* Explicit expand/collapse control. The folder glyph alone was the
              only affordance before, and it was not obvious a row could open. */}
          {entry.is_dir ? (
            <button
              type="button"
              className="nc-caret"
              aria-label={isExpanded ? t('collapse') : t('expand')}
              aria-expanded={isExpanded}
              onClick={(e) => { e.stopPropagation(); toggleDir(entry.path) }}
            >
              {isExpanded ? '▾' : '▸'}
            </button>
          ) : (
            <span className="nc-caret-spacer" />
          )}
          <span style={{ marginRight: '6px' }}>
            {entry.is_dir ? (isExpanded ? '📂' : '📁') : '📄'}
          </span>
          <span
            onClick={() => entry.is_dir && toggleDir(entry.path)}
            style={{ flex: 1, cursor: entry.is_dir ? 'pointer' : 'default', fontSize: '14px' }}
          >
            {entry.path.split('/').pop() || entry.path}
          </span>
          <span className="nc-size-col">{formatBytes(entrySize(entry))}</span>
        </div>
        {entry.is_dir && isExpanded && (childrenByDir.get(entry.path) || []).map(child =>
          renderTreeNode(child, childrenByDir, depth + 1)
        )}
      </div>
    )
  }

  return (
    <>
      <div className="header">
        <div style={{display: 'flex', justifyContent: 'space-between', alignItems: 'center'}}>
          <div>
            <h1>🛡️ {t('appTitle')}</h1>
            <p>{t('appSubtitle')}</p>
          </div>
          <HeaderControls />
        </div>
      </div>

      {picker && (
        <PathPicker
          mode={picker.mode}
          initialPath={picker.initialPath}
          defaultFileName={picker.defaultFileName}
          needBytes={picker.needBytes}
          api={pickerApi}
          onCancel={() => closePicker('')}
          onConfirm={(path) => closePicker(path)}
        />
      )}

      <div className="container">
      {securityWarnings.length > 0 && (
        <div className="security-warning-banner" style={{background:'var(--nc-warn-bg)',border:'1px solid var(--nc-warn-border)',borderRadius:'6px',padding:'10px 14px',margin:'8px'}}>
          <strong>⚠️ {t('securityWarningTitle')}</strong>
          <ul style={{margin:'6px 0 0',paddingLeft:'20px'}}>
            {securityWarnings.map((w,i) => <li key={i} style={{fontSize:'0.9em'}}>{w}</li>)}
          </ul>
        </div>
      )}

        <div className="tabs">
          <div className={`tab ${activeTab === 'servers' ? 'active' : ''}`} onClick={() => setActiveTab('servers')}>
            {t('tabServers')}
          </div>
          <div className={`tab ${activeTab === 'backup' ? 'active' : ''}`} onClick={() => setActiveTab('backup')}>
            {t('tabBackup')}
          </div>
          <div className={`tab ${activeTab === 'restore' ? 'active' : ''}`} onClick={() => setActiveTab('restore')}>
            {t('tabRestore')}
          </div>
          <div className={`tab ${activeTab === 'about' ? 'active' : ''}`} onClick={() => setActiveTab('about')}>
            {t('tabAbout')}
          </div>
        </div>

        {/* PBS Configuration Tab */}
        <div className={`tab-content ${activeTab === 'servers' ? 'active' : ''}`}>
            <div className="card" style={{marginTop: '16px'}}>
              <label style={{fontWeight: 'bold'}}>{t('controlServerSection')}</label>
              <div className="hint-text" style={{margin: '4px 0 8px'}}>{t('controlServerHint')}</div>
              {cpStatus && cpStatus.configured && (
                <div className="cp-status">
                  <span className={'cp-dot ' + (cpStatus.connected ? 'ok' : 'err')}></span>
                  <span className="mono">{cpStatus.server_host}</span>
                  <span>
                    {cpStatus.connected ? t('controlServerConnected') : t('controlServerDisconnected')}
                    {cpStatus.enrolled ? ` · ${t('controlServerAgentId')} ${cpStatus.agent_id}` : ` · ${t('controlServerNotEnrolled')}`}
                  </span>
                  {cpStatus.last_checkin && <span className="hint-text">{t('controlServerLastCheckin')}: {new Date(cpStatus.last_checkin).toLocaleString()}</span>}
                  {!cpStatus.connected && cpStatus.last_error && <span className="hint-text">{cpStatus.last_error}</span>}
                </div>
              )}
              <label>{t('controlServerUrlLabel')}</label>
              <input type="text" placeholder="https://nimbus.example.com" value={cpForm.url} onChange={(e) => setCpForm({...cpForm, url: e.target.value})} />
              <label>{t('controlServerTokenLabel')}</label>
              <input type="password" placeholder={cpStatus && cpStatus.enrolled ? t('controlServerTokenEnrolledPh') : t('controlServerTokenPh')} value={cpForm.token} onChange={(e) => setCpForm({...cpForm, token: e.target.value})} />
              <label>{t('controlServerFpLabel')}</label>
              <input type="text" placeholder={t('controlServerFpPh')} value={cpForm.fp} onChange={(e) => setCpForm({...cpForm, fp: e.target.value})} />
              <button className="btn btn-secondary" style={{marginTop: '8px'}} onClick={saveControlServer}>{t('controlServerSave')}</button>
            </div>
          <h2>🖥️ {t('serversTitle')}</h2>

          {/* Show form first if no servers configured */}
          {pbsServers.length === 0 ? (
            <>
              <div className="info-box" style={{marginBottom: '20px', backgroundColor: 'color-mix(in srgb, var(--nc-accent) 8%, var(--nc-panel-head))', borderLeft: '4px solid var(--nc-accent)'}}>
                👋 <strong>{t('welcomeMessage')}</strong> {t('welcomeText')}<br/>
                {!config.baseurl && (
                  <>
                    <br/>
                    <strong>📦 {t('noPBSYet')}</strong><br/>
                    <a
                      href={`${t('chooseBackupUrl')}?utm_source=NimbusGui&utm_medium=tooling&utm_campaign=version-${appVersion}&utm_content=first-setup`}
                      target="_blank"
                      rel="noopener noreferrer"
                      style={{color: 'var(--nc-accent)', fontWeight: 'bold', textDecoration: 'underline'}}
                    >
                      {t('orderStorage')} →
                    </a>
                  </>
                )}
              </div>

              {/* Add Server Form - Prominent when no servers */}
              <div className="card">
                <h3>➕ {t('addYourServer')}</h3>
              <table style={{width: '100%', marginTop: '15px'}}>
                <thead>
                  <tr>
                    <th>{t('name')}</th>
                    <th>{t('url')}</th>
                    <th>{t('datastore')}</th>
                    <th>{t('status')}</th>
                    <th>{t('actions')}</th>
                  </tr>
                </thead>
                <tbody>
                  {pbsServers.map(server => (
                    <tr key={server.id}>
                      <td>
                        <strong>{server.name}</strong>
                        {server.id === defaultPBSID && <span style={{marginLeft: '5px', color: 'var(--nc-warn)'}}>⭐ {t('default')}</span>}
                        {server.description && <div style={{fontSize: '0.85em', color: 'var(--nc-text-dim)'}}>{server.description}</div>}
                      </td>
                      <td>{server.baseurl}</td>
                      <td>{server.datastore}/{server.namespace || '-'}</td>
                      <td>
                        {serverStatus[server.id] === 'testing' && <span style={{color: 'var(--nc-accent)'}}>🔄 {t('testing')}</span>}
                        {serverStatus[server.id] === 'online' && <span style={{color: 'var(--nc-ok)'}}>🟢 {t('online')}</span>}
                        {serverStatus[server.id] === 'offline' && <span style={{color: 'var(--nc-err)'}}>🔴 {t('offline')}</span>}
                        {!serverStatus[server.id] && <span style={{color: 'var(--nc-text-dim)'}}>⚪ {t('untested')}</span>}
                      </td>
                      <td>
                        <button onClick={() => handleTestPBSConnection(server.id)} style={{marginRight: '5px', padding: '5px 10px', fontSize: '0.9em'}}>
                          🔍 {t('test')}
                        </button>
                        <button onClick={() => handleEditServer(server)} style={{marginRight: '5px', padding: '5px 10px', fontSize: '0.9em'}}>
                          ✏️ {t('edit')}
                        </button>
                        {server.id !== defaultPBSID && (
                          <button className="btn btn-warn" onClick={() => handleSetDefaultPBS(server.id)} style={{marginRight: '5px', padding: '5px 10px', fontSize: '0.9em'}}>
                            ⭐ {t('setAsDefault')}
                          </button>
                        )}
                        <button className="btn btn-danger" onClick={() => handleDeletePBSServer(server.id)} style={{padding: '5px 10px', fontSize: '0.9em'}}>
                          🗑️ {t('delete')}
                        </button>
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            )}
          </div>

          {/* Add/Edit Server Form */}
          <div className="card">
            <h3>{editingServer ? `✏️ ${t('editServer')}` : `➕ ${t('addYourServer')}`}</h3>

            <div className="form-group">
              <label>{t('serverName')}</label>
              <input
                type="text"
                value={serverFormData.name}
                onChange={(e) => setServerFormData({...serverFormData, name: e.target.value})}
                placeholder="SSD Rapide"
              />
            </div>

            {!editingServer && (
              <div className="form-group">
                <label>{t('serverID')}</label>
                <input
                  type="text"
                  value={serverFormData.id}
                  onChange={(e) => setServerFormData({...serverFormData, id: e.target.value})}
                  placeholder="pbs-ssd (laissez vide pour auto-génération)"
                />
              </div>
            )}

            <div className="form-group">
              <label>{t('serverURL')}</label>
              <input
                type="text"
                value={serverFormData.baseurl}
                onChange={(e) => setServerFormData({...serverFormData, baseurl: e.target.value})}
                placeholder="https://pbs-ssd.example.com:8007"
              />
            </div>

            <div className="form-group">
              <label>{t('authID')}</label>
              <input
                type="text"
                value={serverFormData.authid}
                onChange={(e) => setServerFormData({...serverFormData, authid: e.target.value})}
                placeholder="backup@pbs!token-name"
              />
            </div>

            <div className="form-group">
              <label>{t('secret')}</label>
              <input
                type="password"
                value={serverFormData.secret}
                onChange={(e) => setServerFormData({...serverFormData, secret: e.target.value})}
                placeholder={serverFormData.secret_set ? '•••••••• (laisser vide pour conserver le token actuel)' : 'xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx'}
              />
            </div>

            <div className="form-group">
              <label>{t('datastore')}</label>
              <input
                type="text"
                value={serverFormData.datastore}
                onChange={(e) => setServerFormData({...serverFormData, datastore: e.target.value})}
                placeholder="ssd-fast"
              />
            </div>

            <div className="form-group">
              <label>{t('namespace')}</label>
              <input
                type="text"
                value={serverFormData.namespace}
                onChange={(e) => setServerFormData({...serverFormData, namespace: e.target.value})}
                placeholder="clients"
              />
            </div>

            <div className="form-group">
              <label>{t('certFingerprint')}</label>
              <input
                type="text"
                value={serverFormData.certfingerprint}
                onChange={(e) => setServerFormData({...serverFormData, certfingerprint: e.target.value})}
                placeholder="AA:BB:CC:DD:..."
              />
            </div>

            <div className="form-group">
              <label>{t('description')}</label>
              <textarea
                value={serverFormData.description}
                onChange={(e) => setServerFormData({...serverFormData, description: e.target.value})}
                placeholder="Stockage SSD pour backups critiques"
                rows="2"
              />
            </div>

            <div style={{display: 'flex', gap: '10px', marginTop: '20px'}}>
              {editingServer ? (
                <>
                  <button onClick={handleUpdatePBSServer} style={{flex: 1}}>
                    💾 {t('update')}
                  </button>
                  <button className="btn btn-secondary" onClick={handleCancelEdit} style={{flex: 1}}>
                    ❌ {t('cancel')}
                  </button>
                </>
              ) : (
                <button onClick={handleAddPBSServer} style={{flex: 1}}>
                  ➕ {t('addFirstServer')}
                </button>
              )}
            </div>

            <div className="info-box" style={{marginTop: '20px'}}>
              💡 <strong>{t('tipTitle')}</strong> {t('tipAPIToken')}<br/>
              {t('tipAPITokenPath')}
            </div>
          </div>
            </>
          ) : (
            <>
              {/* Multi-PBS info for users with existing servers */}
              <div className="info-box" style={{marginBottom: '20px'}}>
                💡 <strong>{t('multiPBSInfo')}</strong> {t('multiPBSText')}<br/>
                {t('multiPBSExample')}
              </div>

              {/* Server List */}
              <div className="card" style={{marginBottom: '20px'}}>
                <h3>{t('configuredServers')} ({pbsServers.length})</h3>

                <table style={{width: '100%', marginTop: '15px'}}>
                  <thead>
                    <tr>
                      <th>{t('name')}</th>
                      <th>{t('url')}</th>
                      <th>{t('datastore')}</th>
                      <th>{t('status')}</th>
                      <th>{t('actions')}</th>
                    </tr>
                  </thead>
                  <tbody>
                    {pbsServers.map(server => (
                      <tr key={server.id}>
                        <td>
                          <strong>{server.name}</strong>
                          {server.id === defaultPBSID && <span style={{marginLeft: '5px', color: 'var(--nc-warn)'}}>⭐ {t('default')}</span>}
                          {server.description && <div style={{fontSize: '0.85em', color: 'var(--nc-text-dim)'}}>{server.description}</div>}
                        </td>
                        <td>{server.baseurl}</td>
                        <td>{server.datastore}/{server.namespace || '-'}</td>
                        <td>
                          {serverStatus[server.id] === 'testing' && <span style={{color: 'var(--nc-accent)'}}>🔄 {t('statusTesting')}</span>}
                          {serverStatus[server.id] === 'online' && <span style={{color: 'var(--nc-ok)'}}>🟢 {t('statusOnline')}</span>}
                          {serverStatus[server.id] === 'offline' && <span style={{color: 'var(--nc-err)'}}>🔴 {t('statusOffline')}</span>}
                          {!serverStatus[server.id] && <span style={{color: 'var(--nc-text-dim)'}}>⚪ {t('statusNotTested')}</span>}
                        </td>
                        <td>
                          <button onClick={() => handleTestPBSConnection(server.id)} style={{marginRight: '5px', padding: '5px 10px', fontSize: '0.9em'}}>
                            🔍 {t('testBtn')}
                          </button>
                          <button onClick={() => handleEditServer(server)} style={{marginRight: '5px', padding: '5px 10px', fontSize: '0.9em'}}>
                            ✏️ {t('editBtn')}
                          </button>
                          {server.id !== defaultPBSID && (
                            <button className="btn btn-warn" onClick={() => handleSetDefaultPBS(server.id)} style={{marginRight: '5px', padding: '5px 10px', fontSize: '0.9em'}}>
                              ⭐ {t('setDefaultBtn')}
                            </button>
                          )}
                          <button className="btn btn-danger" onClick={() => handleDeletePBSServer(server.id)} style={{padding: '5px 10px', fontSize: '0.9em'}}>
                            🗑️ {t('deleteBtn')}
                          </button>
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>

              {/* Add/Edit Server Form */}
              <div className="card">
                <h3>{editingServer ? `✏️ ${t('editServer')}` : `➕ ${t('addAnotherServer')}`}</h3>

                <div className="form-group">
                  <label>{t('serverName')}</label>
                  <input
                    type="text"
                    value={serverFormData.name}
                    onChange={(e) => setServerFormData({...serverFormData, name: e.target.value})}
                    placeholder="SSD Rapide"
                  />
                </div>

                {!editingServer && (
                  <div className="form-group">
                    <label>{t('serverID')}</label>
                    <input
                      type="text"
                      value={serverFormData.id}
                      onChange={(e) => setServerFormData({...serverFormData, id: e.target.value})}
                      placeholder="pbs-ssd (laissez vide pour auto-génération)"
                    />
                  </div>
                )}

                <div className="form-group">
                  <label>{t('serverURL')}</label>
                  <input
                    type="text"
                    value={serverFormData.baseurl}
                    onChange={(e) => setServerFormData({...serverFormData, baseurl: e.target.value})}
                    placeholder="https://pbs-ssd.example.com:8007"
                  />
                </div>

                <div className="form-group">
                  <label>{t('authID')}</label>
                  <input
                    type="text"
                    value={serverFormData.authid}
                    onChange={(e) => setServerFormData({...serverFormData, authid: e.target.value})}
                    placeholder="backup@pbs!token-name"
                  />
                </div>

                <div className="form-group">
                  <label>{t('secret')}</label>
                  <input
                    type="password"
                    value={serverFormData.secret}
                    onChange={(e) => setServerFormData({...serverFormData, secret: e.target.value})}
                    placeholder={serverFormData.secret_set ? '•••••••• (laisser vide pour conserver le token actuel)' : 'xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx'}
                  />
                </div>

                <div className="form-group">
                  <label>{t('datastore')}</label>
                  <input
                    type="text"
                    value={serverFormData.datastore}
                    onChange={(e) => setServerFormData({...serverFormData, datastore: e.target.value})}
                    placeholder="ssd-fast"
                  />
                </div>

                <div className="form-group">
                  <label>{t('namespace')}</label>
                  <input
                    type="text"
                    value={serverFormData.namespace}
                    onChange={(e) => setServerFormData({...serverFormData, namespace: e.target.value})}
                    placeholder="clients"
                  />
                </div>

                <div className="form-group">
                  <label>{t('certFingerprint')}</label>
                  <input
                    type="text"
                    value={serverFormData.certfingerprint}
                    onChange={(e) => setServerFormData({...serverFormData, certfingerprint: e.target.value})}
                    placeholder="AA:BB:CC:DD:..."
                  />
                </div>

                <div className="form-group">
                  <label>{t('description')}</label>
                  <textarea
                    value={serverFormData.description}
                    onChange={(e) => setServerFormData({...serverFormData, description: e.target.value})}
                    placeholder="Stockage SSD pour backups critiques"
                    rows="2"
                  />
                </div>

                <div style={{display: 'flex', gap: '10px', marginTop: '20px'}}>
                  {editingServer ? (
                    <>
                      <button onClick={handleUpdatePBSServer} style={{flex: 1}}>
                        💾 {t('update')}
                      </button>
                      <button className="btn btn-secondary" onClick={handleCancelEdit} style={{flex: 1}}>
                        ❌ {t('cancel')}
                      </button>
                    </>
                  ) : (
                    <button onClick={handleAddPBSServer} style={{flex: 1}}>
                      ➕ {t('addServer')}
                    </button>
                  )}
                </div>
              </div>
            </>
          )}

          {status.visible && activeTab === 'servers' && (
            <div className={`status ${status.type} visible`}>{status.message}</div>
          )}
        </div>

        {/* Backup Tab */}
        <div className={`tab-content ${activeTab === 'backup' ? 'active' : ''}`}>
          <h2>{t('backupTitle')}</h2>

          <div className="form-group">
            <label>{t('backupType')}</label>
            <select value={backupType} onChange={(e) => setBackupType(e.target.value)}>
              <option value="directory">📁 {t('backupTypeDirectory')}</option>
              <option value="machine">💾 {t('backupTypeMachine')}</option>
            </select>
          </div>

          {/* Backup Mode Toggle */}
          <div className="form-group">
            <label>{t('executionMode')}</label>
            <div className="nc-seg" style={{marginTop: '10px'}}>
              <button
                type="button"
                className={backupMode === 'oneshot' ? 'active' : ''}
                onClick={() => setBackupMode('oneshot')}
              >
                <span className="compact-text-long">⚡ {t('oneshotMode')}</span>
                <span className="compact-text-short">⚡ {t('oneshotModeShort')}</span>
              </button>
              <button
                type="button"
                className={backupMode === 'scheduled' ? 'active' : ''}
                onClick={() => setBackupMode('scheduled')}
              >
                <span className="compact-text-long">📅 {t('scheduledMode')}</span>
                <span className="compact-text-short">📅 {t('scheduledModeShort')}</span>
              </button>
            </div>
          </div>

          {/* Scheduling Options */}
          {backupMode === 'scheduled' && (
            <div className="card" style={{marginTop: '20px', padding: '20px'}}>
              <h3 style={{marginTop: 0}}>⏰ {t('schedulingConfig')}</h3>

              {editingJobId && (
                <div className="info-box" style={{backgroundColor: 'var(--nc-warn-bg)', borderColor: 'var(--nc-warn-border)', marginBottom: '15px'}}>
                  ✏️ <strong>{t('editMode')}</strong> - {t('editModeText')}
                </div>
              )}

              <div className="form-group">
                <label>{t('dailyExecutionTime')}</label>
                <input
                  type="time"
                  value={scheduleTime}
                  onChange={(e) => setScheduleTime(e.target.value)}
                  style={{width: '200px', padding: '10px', fontSize: '16px'}}
                />
              </div>

              <div className="form-group">
                <label style={{display: 'flex', alignItems: 'center', gap: '10px', cursor: 'pointer'}}>
                  <input
                    type="checkbox"
                    checked={runAtStartup}
                    onChange={(e) => setRunAtStartup(e.target.checked)}
                    style={{width: '20px', height: '20px', cursor: 'pointer'}}
                  />
                  <span>🚀 {t('runAtStartup')}</span>
                </label>
              </div>

              <div className="info-box" style={{backgroundColor: 'color-mix(in srgb, var(--nc-accent) 8%, var(--nc-panel-head))'}}>
                💡 {t('schedulingInfo')} <strong>{scheduleTime}</strong>
                {runAtStartup && <><br/>{t('andAtStartup')}</>}
              </div>
            </div>
          )}

          {backupType === 'directory' ? (
            <div className="form-group">
              <label>{t('directoriesToBackup')}</label>
              <textarea
                value={backupDirs}
                onChange={(e) => {
                  setBackupDirs(e.target.value)
                  // Update config.backupdir with first directory for compatibility
                  const dirs = e.target.value.split('\n').map(d => d.trim()).filter(d => d)
                  setConfig({...config, backupdir: dirs[0] || ''})
                }}
                rows="4"
                placeholder="C:\Data&#10;C:\Users&#10;D:\Documents"
              />
            </div>
          ) : (
            <>
              <div className="info-box" style={{backgroundColor: 'var(--nc-warn-bg)', borderColor: 'var(--nc-warn-border)'}}>
                ⚠️ <strong>{t('machineBackupWarning')}</strong><br/>
                {t('machineBackupWarningHint')}
              </div>

              <div className="form-group">
                <label>{t('physicalDisksToBackup')}</label>
                {disksLoading ? (
                  <div style={{padding: '10px', backgroundColor: 'var(--nc-panel-head)', borderRadius: '4px'}}>
                    🔍 {t('loadingDisks')}
                  </div>
                ) : disksError ? (
                  <div style={{padding: '10px', backgroundColor: 'var(--nc-err-bg)', borderRadius: '4px'}}>
                    ❌ {t('diskDetectionError')}: {disksError}
                  </div>
                ) : physicalDisks.length === 0 ? (
                  <div style={{padding: '10px', backgroundColor: 'var(--nc-warn-bg)', borderRadius: '4px'}}>
                    ⚠️ {t('noDisksDetected')}
                  </div>
                ) : (
                  <div style={{display: 'flex', flexDirection: 'column', gap: '8px'}}>
                    {physicalDisks.map(disk => (
                      <label key={disk.path} style={{display: 'flex', alignItems: 'center', gap: '8px'}}>
                        <input
                          type="checkbox"
                          checked={selectedDrives.includes(disk.path)}
                          onChange={(e) => {
                            if (e.target.checked) {
                              setSelectedDrives([...selectedDrives, disk.path])
                            } else {
                              setSelectedDrives(selectedDrives.filter(d => d !== disk.path))
                            }
                          }}
                        />
                        {disk.label}
                      </label>
                    ))}
                  </div>
                )}
              </div>

              <div className="form-group">
                <label>{t('filesToExclude')}</label>
                <textarea
                  value={excludeList}
                  onChange={(e) => setExcludeList(e.target.value)}
                  rows="4"
                  placeholder="*.tmp&#10;*.log&#10;C:\Windows\Temp"
                />
              </div>
            </>
          )}

          <div className="form-group">
            <label>{t('backupID')}</label>
            <input
              type="text"
              value={config['backup-id']}
              onChange={(e) => setConfig({...config, 'backup-id': e.target.value})}
              placeholder={t('backupIDPlaceholder')}
            />
          </div>

          <div className="form-group">
            <label>
              <input
                type="checkbox"
                checked={config.usevss}
                onChange={(e) => setConfig({...config, usevss: e.target.checked})}
              />
              {t('useVSS')}
            </label>
            <div style={{marginTop: '12px'}}>
              <label>{t('uploadLimitLabel')}</label>
              <input
                type="number"
                min="0"
                step="1"
                value={config.upload_limit_mbps || 0}
                onChange={(e) => setConfig({...config, upload_limit_mbps: e.target.value})}
              />
              <div style={{fontSize: '0.85em', color: 'var(--nc-text-dim)', marginTop: '4px'}}>{t('uploadLimitHint')}</div>
            </div>
            {exchangeStatus.installed && (
              <div className="form-group" style={exchangeStatus.highlight_setting ? {marginTop:'16px',background:'var(--nc-warn-bg)',border:'1px solid var(--nc-warn-border)',borderRadius:'6px',padding:'10px 14px'} : {marginTop:'16px'}}>
                <label style={{fontWeight:'bold'}}>{t('exchangeSection')} {exchangeStatus.version ? '(' + exchangeStatus.version + ')' : ''}</label>
                <div style={{fontSize:'0.85em',color:'var(--nc-text-dim)',margin:'4px 0 8px'}}>
                  {exchangeStatus.highlight_setting ? '⚠️ ' + t('exchangeDetectedHint') : t('exchangeHint')}
                </div>
                <label>
                  <input type="checkbox" checked={!!config.exchange_aware} onChange={(e) => setConfig({...config, exchange_aware: e.target.checked})} />
                  {t('exchangeAwareLabel')}
                </label>
                <div style={exchangeLogMode.queried && exchangeLogMode.logs_accumulate && !config.exchange_log_truncation ? {marginTop:'10px',background:'var(--nc-warn-bg)',border:'1px solid var(--nc-warn-border)',borderRadius:'6px',padding:'8px 12px'} : {marginTop:'10px'}}>
                  <label>
                    <input type="checkbox" checked={!!config.exchange_log_truncation} onChange={(e) => setConfig({...config, exchange_log_truncation: e.target.checked})} />
                    {t('exchangeLogTruncationLabel')}
                  </label>
                  <div style={{fontSize:'0.82em',color:'var(--nc-text-dim)',marginTop:'4px'}}>
                    {t('exchangeLogTruncationHint')}
                    {exchangeLogMode.queried && (
                      <div style={{marginTop:'4px'}}>
                        {exchangeLogMode.logs_accumulate ? '⚠️ ' + t('exchangeLogsAccumulate') : '✓ ' + t('exchangeLogsCircular')}
                        {exchangeLogMode.detail ? ' — ' + exchangeLogMode.detail : ''}
                      </div>
                    )}
                  </div>
                </div>
              </div>
            )}
            {config.usevss && systemInfo.mode === 'Standalone' && !systemInfo.is_admin && (
              <div className="info-box" style={{marginTop: '10px', backgroundColor: 'var(--nc-warn-bg)', borderColor: 'var(--nc-warn-border)'}}>
                ⚠️ <strong>{t('vssAdminRequired')}</strong><br/>
                {t('vssAdminHint')}
              </div>
            )}
            {config.usevss && systemInfo.service_available && (
              <div className="info-box" style={{marginTop: '10px', backgroundColor: 'color-mix(in srgb, var(--nc-accent) 8%, var(--nc-panel-head))', borderColor: 'color-mix(in srgb, var(--nc-accent) 35%, var(--nc-border))'}}>
                ℹ️ <strong>{t('vssServiceAvailable')}</strong><br/>
                {t('vssServiceHint')}
              </div>
            )}
          </div>

          {backupType === 'directory' && backupMode === 'oneshot' && (
            <div className="form-group">
              <label>
                <input
                  type="checkbox"
                  checked={splitFirstBackup}
                  onChange={(e) => setSplitFirstBackup(e.target.checked)}
                />
                {t('splitFirstBackup')}
              </label>
              <div className="info-box" style={{marginTop: '10px', backgroundColor: 'var(--nc-panel-head)', borderColor: 'var(--nc-border)'}}>
                ℹ️ {t('splitFirstBackupHint')}
              </div>
            </div>
          )}

          {progress > 0 && progress < 100 && (
            <div style={{marginTop: '20px', marginBottom: '20px', padding: '15px', backgroundColor: 'var(--nc-panel-head)', borderRadius: '8px', border: '1px solid var(--nc-border)'}}>
              <div style={{display: 'flex', justifyContent: 'space-between', marginBottom: '10px'}}>
                <strong style={{fontSize: '15px'}}>📊 {t('backupProgress')}</strong>
                <span style={{fontSize: '18px', fontWeight: 'bold', color: 'var(--nc-accent)'}}>{progress}%</span>
              </div>

              <div className="progress" style={{height: '30px', marginBottom: '12px'}}>
                <div
                  className="progress-bar"
                  style={{
                    width: `${progress}%`,
                    fontSize: '14px',
                    lineHeight: '30px',
                    transition: 'width 0.3s ease',
                    fontWeight: 'bold'
                  }}
                >
                  {progress}%
                </div>
              </div>

              <div style={{display: 'grid', gridTemplateColumns: '1fr 1fr', gap: '10px', marginBottom: '10px'}}>
                {backupStats.eta !== null && (
                  <div style={{fontSize: '13px', color: 'var(--nc-text-dim)'}}>
                    ⏱️ <strong>{t('timeRemaining')}</strong> {Math.floor(backupStats.eta / 60)}m {backupStats.eta % 60}s
                  </div>
                )}
                {backupStats.speed > 0 && (
                  <div style={{fontSize: '13px', color: 'var(--nc-text-dim)'}}>
                    ⚡ <strong>{t('speed')}</strong> {backupStats.speed.toFixed(1)}%/s
                  </div>
                )}
                {backupStats.startTime && (
                  <div style={{fontSize: '13px', color: 'var(--nc-text-dim)'}}>
                    ⏰ <strong>{t('elapsedTime')}</strong> {Math.floor((Date.now() - backupStats.startTime) / 1000)}s
                  </div>
                )}
                {backupStats.bytesDone > 0 && (
                  <div style={{fontSize: '13px', color: 'var(--nc-text-dim)'}}>
                    📦 <strong>Données :</strong> {Math.round(backupStats.bytesDone / 1048576)}
                    {backupStats.bytesTotal > 0 ? ` / ${Math.round(backupStats.bytesTotal / 1048576)}` : ''} MB
                  </div>
                )}
                {(backupStats.newChunks > 0 || backupStats.reusedChunks > 0) && (
                  <div style={{fontSize: '13px', color: 'var(--nc-text-dim)'}}>
                    🧩 <strong>Chunks :</strong> {backupStats.newChunks} new · {backupStats.reusedChunks} reused
                    {backupStats.failedChunks > 0 ? (
                      <span style={{color: 'var(--nc-err)', fontWeight: 'bold'}}> · {backupStats.failedChunks} échoués</span>
                    ) : ''}
                  </div>
                )}
                {backupStats.currentDir && (
                  <div style={{fontSize: '13px', color: 'var(--nc-text-dim)', gridColumn: '1 / -1', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap'}}>
                    📁 <strong>Dossier :</strong> {backupStats.currentDir}
                  </div>
                )}
              </div>

              {status.message && status.type === 'info' && (
                <div style={{marginTop: '10px', padding: '8px', backgroundColor: 'var(--nc-panel)', borderRadius: '4px', fontSize: '13px', color: 'var(--nc-text-dim)', border: '1px solid var(--nc-border-soft)'}}>
                  {status.message}
                </div>
              )}
            </div>
          )}

          <button className="btn" onClick={handleStartBackup} disabled={progress > 0 && progress < 100}>
            {backupMode === 'oneshot'
              ? (progress > 0 && progress < 100 ? `⏳ ${t('backupInProgress')}` : `🚀 ${t('startBackup')}`)
              : (editingJobId ? `✏️ ${t('updateSchedule')}` : `💾 ${t('saveSchedule')}`)
            }
          </button>
          {backupMode === 'oneshot' && (
            <button className="btn btn-secondary" onClick={() => setProgress(0)} disabled={progress === 0}>{t('stopBackup')}</button>
          )}
          {backupMode === 'scheduled' && editingJobId && (
            <button className="btn btn-secondary" onClick={() => {
              setEditingJobId(null)
              setScheduleTime('02:00')
              setRunAtStartup(false)
              setBackupDirs('')
              setExcludeList('')
              setBackupType('directory')
              setActiveTab('scheduled')
              showStatus(`✖️ ${t('statusEditCancelled')}`, 'info')
            }}>
              ✖️ {t('cancel')}
            </button>
          )}

          {/* Scheduled Jobs List */}
          {backupMode === 'scheduled' && scheduledJobs.length > 0 && (
            <div className="card" style={{marginTop: '30px'}}>
              <h3 style={{marginTop: 0}}>📅 {t('scheduledJobs')}</h3>
              {scheduledJobs.map(job => (
                <div key={job.id} style={{
                  padding: '15px',
                  marginBottom: '10px',
                  backgroundColor: 'var(--nc-panel-head)',
                  borderRadius: '8px',
                  border: '1px solid var(--nc-border)'
                }}>
                  <div style={{display: 'flex', justifyContent: 'space-between', alignItems: 'center'}}>
                    <div>
                      <strong>{job.name}</strong>
                      <div style={{fontSize: '14px', color: 'var(--nc-text-dim)', marginTop: '5px'}}>
                        ⏰ {job.scheduleTime} {job.runAtStartup && '• 🚀 Au démarrage'}
                      </div>
                      <div style={{fontSize: '13px', color: 'var(--nc-text-dim)', marginTop: '3px'}}>
                        📁 {job.backupDirs.join(', ')}
                      </div>
                    </div>
                    <div style={{display: 'flex', gap: '10px'}}>
                      <button
                        className="btn"
                        style={{padding: '8px 15px', fontSize: '14px'}}
                        onClick={() => {
                          // Load job data into form for editing
                          setEditingJobId(job.id)
                          setBackupMode('scheduled')
                          setScheduleTime(job.scheduleTime)
                          setRunAtStartup(job.runAtStartup)
                          setBackupDirs(job.backupDirs.join('\n'))
                          setSelectedDrives(job.driveLetters || [])
                          setConfig({...config, 'backup-id': job.backupId, usevss: job.useVSS})
                          setBackupType(job.backupType)
                          setExcludeList(job.excludeList.join('\n'))
                          // Switch to backup tab to show the form
                          setActiveTab('backup')
                          showStatus(`✏️ ${t('editModeInfo')}`, 'info')
                          window.scrollTo({top: 0, behavior: 'smooth'})
                        }}
                      >
                        ✏️ {t('editJob')}
                      </button>
                      <button
                        className="btn btn-secondary"
                        style={{padding: '8px 15px', fontSize: '14px'}}
                        onClick={async () => {
                          try {
                            await DeleteScheduledJob(job.id)
                            setScheduledJobs(scheduledJobs.filter(j => j.id !== job.id))
                            showStatus(t('statusJobDeleted'), 'success')
                            // Cancel edit mode if deleting the job being edited
                            if (editingJobId === job.id) {
                              setEditingJobId(null)
                            }
                          } catch (err) {
                            showStatus(`❌ Erreur: ${err}`, 'error')
                          }
                        }}
                      >
                        🗑️ {t('deleteJob')}
                      </button>
                    </div>
                  </div>
                </div>
              ))}
            </div>
          )}

          {/* Job History */}
          {jobHistory.length > 0 && (
            <div className="card" style={{marginTop: '30px'}}>
              <h3 style={{marginTop: 0}}>📜 {t('backupHistory')}</h3>
              <div style={{maxHeight: '400px', overflowY: 'auto'}}>
                {jobHistory.slice(0, 6).map(job => (
                  <div key={job.id} style={{
                    padding: '15px',
                    marginBottom: '10px',
                    backgroundColor: job.status === 'success' ? 'color-mix(in srgb, var(--nc-ok) 12%, var(--nc-panel))' : job.status === 'failed' ? 'var(--nc-err-bg)' : 'var(--nc-warn-bg)',
                    borderRadius: 'var(--nc-radius)',
                    border: `1px solid ${job.status === 'success' ? 'color-mix(in srgb, var(--nc-ok) 45%, var(--nc-border))' : job.status === 'failed' ? 'color-mix(in srgb, var(--nc-err) 45%, var(--nc-border))' : 'var(--nc-warn-border)'}`
                  }}>
                    <div style={{display: 'flex', justifyContent: 'space-between', alignItems: 'center'}}>
                      <div style={{flex: 1}}>
                        <div style={{display: 'flex', alignItems: 'center', gap: '10px'}}>
                          <span style={{fontSize: '20px'}}>
                            {job.status === 'success' ? '✅' : job.status === 'failed' ? '❌' : '⏳'}
                          </span>
                          <strong>{job.name}</strong>
                        </div>
                        <div style={{fontSize: '13px', color: 'var(--nc-text-dim)', marginTop: '5px', marginLeft: '30px'}}>
                          🕐 {new Date(job.timestamp).toLocaleString('fr-FR')}
                        </div>
                        {job.message && (
                          <div style={{fontSize: '13px', color: 'var(--nc-text-dim)', marginTop: '5px', marginLeft: '30px'}}>
                            💬 {localizeMessage(job.message)}
                          </div>
                        )}
                      </div>
                      {job.status === 'failed' && (
                        <button
                          className="btn"
                          style={{padding: '8px 15px', fontSize: '14px'}}
                          onClick={() => {
                            // Re-run failed job
                            setBackupDirs(job.backupDirs.join('\n'))
                            setConfig({...config, 'backup-id': job.backupId, usevss: job.useVSS})
                            showStatus(t('configLoaded'), 'success')
                            window.scrollTo({top: 0, behavior: 'smooth'})
                          }}
                        >
                          🔄 {t('rerun')}
                        </button>
                      )}
                    </div>
                  </div>
                ))}
              </div>
            </div>
          )}

          {status.visible && activeTab === 'backup' && (
            <div className={`status ${status.type} visible`}>{status.message}</div>
          )}
        </div>

        {/* Restore Tab */}
        <div className={`tab-content ${activeTab === 'restore' ? 'active' : ''}`}>
          <h2>{t('restoreTitle')}</h2>

          {/* BETA Warning */}
          <div className="warn-box" style={{marginBottom: '20px'}}>
            <strong>⚠️ {t('restoreBetaTitle')}</strong>
            <p style={{margin: '8px 0 0 0', fontSize: '14px'}}>
              {t('restoreBetaIntro')}
              <br/>✅ {t('restoreBetaFilesDirs')}
              <br/>✅ {t('restoreBetaSelective')}
              <br/>✅ {t('restoreBetaTimestamps')}
              <br/>❌ {t('restoreBetaACLs')}
            </p>
          </div>

          {/* PBS server selector + Backup ID */}
          <div style={{display: 'flex', gap: '15px', flexWrap: 'wrap'}}>
            <div className="form-group" style={{flex: '1 1 240px'}}>
              <label>{t('restorePBSServer')}</label>
              <select
                value={restorePBSID}
                onChange={(e) => {
                  setRestorePBSID(e.target.value)
                  setShowSnapshots(false)
                  setSelectedSnapshot(null)
                  setSnapshotEntries([])
                }}
              >
                {pbsServers.length === 0 && <option value="">{t('noPBSServer')}</option>}
                {pbsServers.map(s => (
                  <option key={s.id} value={s.id}>
                    {s.name} {s.id === defaultPBSID ? '⭐' : ''}
                  </option>
                ))}
              </select>
            </div>
            <div className="form-group" style={{flex: '2 1 320px'}}>
              <label>{t('backupIDToRestore')}</label>
              <input
                type="text"
                value={restoreBackupId || hostname}
                onChange={(e) => setRestoreBackupId(e.target.value)}
                placeholder={hostname || "hostname ou ID personnalisé"}
              />
            </div>
          </div>

          <button className="btn" onClick={handleListSnapshots}>📋 {t('listSnapshots')}</button>
          <button
            className="btn"
            type="button"
            onClick={() => setShowSearch(v => !v)}
            style={{marginLeft: '8px'}}
          >
            🔎 {t('searchTitle')}
          </button>

          {showSearch && (
            <div style={{
              marginTop: '16px',
              padding: '14px',
              border: '1px solid var(--nc-border)',
              borderRadius: '8px',
              backgroundColor: 'var(--nc-panel-head)'
            }}>
              <p style={{fontSize: '13px', color: 'var(--nc-text-dim)', marginTop: 0}}>
                {t('searchHint').replace('{prefix}', (restoreBackupId || hostname || '?'))}
              </p>

              <div style={{display: 'flex', gap: '12px', flexWrap: 'wrap', alignItems: 'flex-end'}}>
                <div className="form-group" style={{flex: '2 1 280px', margin: 0}}>
                  <label>{t('searchQueryLabel')}</label>
                  <input
                    type="text"
                    value={searchQuery}
                    onChange={(e) => setSearchQuery(e.target.value)}
                    placeholder={t('searchQueryPlaceholder')}
                    onKeyDown={(e) => { if (e.key === 'Enter' && !searchRunning) handleSearch() }}
                  />
                </div>
                <div className="form-group" style={{flex: '1 1 160px', margin: 0}}>
                  <label>{t('searchModeLabel')}</label>
                  <select value={searchMode} onChange={(e) => setSearchMode(e.target.value)}>
                    <option value="name">{t('searchModeName')}</option>
                    <option value="regex">{t('searchModeRegex')}</option>
                    <option value="path">{t('searchModePath')}</option>
                  </select>
                </div>
              </div>

              <div style={{display: 'flex', gap: '12px', flexWrap: 'wrap', alignItems: 'flex-end', marginTop: '10px'}}>
                <div className="form-group" style={{flex: '1 1 150px', margin: 0}}>
                  <label>{t('searchFrom')}</label>
                  <input type="date" value={searchFrom} onChange={(e) => setSearchFrom(e.target.value)} />
                </div>
                <div className="form-group" style={{flex: '1 1 150px', margin: 0}}>
                  <label>{t('searchTo')}</label>
                  <input type="date" value={searchTo} onChange={(e) => setSearchTo(e.target.value)} />
                </div>
                <label style={{display: 'flex', alignItems: 'center', gap: '8px', flex: '2 1 260px', fontSize: '14px'}}>
                  <input
                    type="checkbox"
                    checked={searchAssembleMissing}
                    onChange={(e) => setSearchAssembleMissing(e.target.checked)}
                  />
                  {t('searchAssembleMissing')}
                </label>
              </div>

              <div style={{marginTop: '12px', display: 'flex', gap: '8px', alignItems: 'center'}}>
                <button className="btn" type="button" onClick={handleSearch} disabled={searchRunning}>
                  {searchRunning ? `⏳ ${t('searching')}` : `🔎 ${t('searchButton')}`}
                </button>
                {searchRunning && (
                  <button className="btn btn-danger" type="button" onClick={handleCancelSearch}>
                    ✖ {t('cancel')}
                  </button>
                )}
                {searchRunning && (
                  <span style={{fontSize: '13px', color: 'var(--nc-text-dim)'}}>
                    {searchProgress.percent}% — {searchProgress.message}
                  </span>
                )}
              </div>

              {searchResult && (
                <div style={{marginTop: '14px'}}>
                  <p style={{fontSize: '13px', color: 'var(--nc-text)', margin: '0 0 6px 0'}}>
                    {t('searchSummary')
                      .replace('{hits}', searchResult.hits ? searchResult.hits.length : 0)
                      .replace('{searched}', searchResult.snapshots_searched || 0)
                      .replace('{assembled}', searchResult.snapshots_assembled || 0)}
                    {searchResult.truncated ? ` ⚠️ ${t('searchTruncated').replace('{max}', 5000)}` : ''}
                    {searchResult.cancelled ? ` ⚠️ ${t('searchCancelled')}` : ''}
                  </p>
                  {searchResult.snapshots_in_range === 0 && (
                    <p style={{fontSize: '12px', color: 'var(--nc-warn)', margin: '0 0 8px 0'}}>
                      💡 {t('searchNoSnapshotsInRange')}
                    </p>
                  )}
                  {(searchResult.snapshots_skipped > 0 && !searchAssembleMissing) && (
                    <p style={{fontSize: '12px', color: 'var(--nc-warn)', margin: '0 0 8px 0'}}>
                      ⚠️ {t('searchSkippedWarning').replace('{n}', searchResult.snapshots_skipped)}
                    </p>
                  )}

                  <div style={{
                    border: '1px solid var(--nc-border)',
                    borderRadius: '8px',
                    maxHeight: '320px',
                    overflowY: 'auto',
                    backgroundColor: 'var(--nc-panel)'
                  }}>
                    {(!searchResult.hits || searchResult.hits.length === 0) ? (
                      <p style={{padding: '12px', color: 'var(--nc-text-dim)'}}>{t('searchNoResults')}</p>
                    ) : (
                      searchResult.hits.map((hit, idx) => (
                        <div
                          key={`${hit.backup_id}-${hit.snapshot_time}-${hit.path}-${idx}`}
                          title={hit.origin_path || hit.path}
                          style={{
                            display: 'flex', alignItems: 'center', gap: '10px',
                            padding: '6px 10px', borderBottom: '1px solid var(--nc-border-soft)', fontSize: '13px'
                          }}
                        >
                          <span style={{fontSize: '15px'}}>{hit.is_dir ? '📁' : '📄'}</span>
                          <div style={{flex: 1, minWidth: 0}}>
                            <div style={{fontWeight: 600, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap'}}>
                              {hit.path.split('/').pop() || hit.path}
                            </div>
                            <div style={{color: 'var(--nc-text-dim)', fontSize: '12px', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap'}}>
                              {hit.origin_path || hit.path}
                            </div>
                            <div style={{color: 'var(--nc-text-dim)', fontSize: '11px'}}>
                              {hit.backup_id} · {new Date(hit.snapshot_time * 1000).toLocaleString()}
                              {hit.is_dir ? '' : ` · ${formatBytes(hit.size)}`}
                            </div>
                          </div>
                          <button
                            className="btn"
                            type="button"
                            onClick={() => handleRestoreHit(hit)}
                            style={{padding: '4px 10px', fontSize: '12px'}}
                          >
                            {t('searchRestoreThis')}
                          </button>
                        </div>
                      ))
                    )}
                  </div>
                </div>
              )}
            </div>
          )}

          {showSnapshots && (
            <div style={{marginTop: '20px'}}>
              <h3>{t('availableSnapshots')}</h3>
              <div className="grid">
                {snapshots.length === 0 ? (
                  <p style={{color: 'var(--nc-text-dim)'}}>{t('noSnapshotFound')}</p>
                ) : (
                  snapshots.map((snap, idx) => {
                    const isActive = selectedSnapshot && selectedSnapshot.id === snap.id && selectedSnapshot.backup_id === snap.backup_id
                    return (
                      <div
                        key={idx}
                        className={'card selectable' + (isActive ? ' nc-selected' : '')}
                        onClick={() => handleSelectSnapshot(snap)}
                      >
                        <h3>📸 {snap.time}</h3>
                        <p style={{color: 'var(--nc-text-dim)', fontSize: '14px', marginTop: '5px'}}>
                          {snap.backup_id}<br/>
                          {isVolumeSnapshot(snap)
                            ? <span>💿 {t('typeVolume')}</span>
                            : <span>📁 {t('typeFile')}</span>}
                        </p>
                        <button className="btn" style={{marginTop: '10px', width: '100%'}}>
                          {isActive ? `✓ ${t('snapshotSelected')}` : t('selectSnapshot')}
                        </button>
                      </div>
                    )
                  })
                )}
              </div>
            </div>
          )}

          {/* Backup origin banner — driven by the .nimbus_backup_meta.json sidecar */}
          {selectedSnapshot && snapshotMeta && (
            <div style={{
              marginTop: '20px',
              padding: '10px 14px',
              border: '1px solid color-mix(in srgb, var(--nc-accent) 35%, var(--nc-border))',
              backgroundColor: 'color-mix(in srgb, var(--nc-accent) 8%, var(--nc-panel-head))',
              borderRadius: '8px',
              fontSize: '13px',
              color: 'var(--nc-text)',
              display: 'grid',
              gridTemplateColumns: 'auto 1fr',
              columnGap: '12px',
              rowGap: '4px'
            }}>
              <strong>{t('metaOriginalPath') || 'Source d\'origine'}</strong>
              <span style={{fontFamily: 'monospace'}}>{snapshotMeta.original_path || '—'}</span>
              <strong>{t('metaHostname') || 'Machine'}</strong>
              <span>{snapshotMeta.hostname || '—'}{snapshotMeta.os ? ` (${snapshotMeta.os})` : ''}{snapshotMeta.vss_used ? ' · VSS' : ''}</span>
              <strong>{t('metaBackupTime') || 'Sauvegardé le'}</strong>
              <span>{snapshotMeta.backup_time || '—'}{snapshotMeta.client_version ? ` · client ${snapshotMeta.client_version}` : ''}</span>
            </div>
          )}

          {/* Snapshot navigation tree */}
          {selectedSnapshot && (
            <div style={{marginTop: '24px'}}>
              <div style={{display: 'flex', alignItems: 'center', gap: '12px', flexWrap: 'wrap'}}>
                <h3 style={{margin: 0}}>📂 {t('snapshotContents')} — {selectedSnapshot.time}</h3>
                <button
                  className="btn"
                  type="button"
                  onClick={handleReloadSnapshot}
                  title={t('reloadTreeHint') || 'Bypass local cache and re-download the snapshot tree'}
                  style={{padding: '4px 10px', fontSize: '13px'}}
                >
                  🔄 {t('reloadTree') || 'Recharger'}
                </button>
              </div>
              <p style={{fontSize: '13px', color: 'var(--nc-text-dim)', marginBottom: '8px', marginTop: '6px'}}>
                {t('treeHint')}
              </p>
              <div style={{
                border: '1px solid var(--nc-border)',
                borderRadius: '8px',
                maxHeight: '360px',
                overflowY: 'auto',
                backgroundColor: 'var(--nc-panel)'
              }}>
                {snapshotEntries.length === 0 && selectedSnapshot && isVolumeSnapshot(selectedSnapshot) ? (
                  <div style={{padding: '12px'}}>
                    <p style={{margin: 0, fontWeight: 600, color: 'var(--nc-accent)'}}>💿 {t('volumeSnapTitle')}</p>
                    <p style={{margin: '6px 0 10px', fontSize: '13px', color: 'var(--nc-text-dim)'}}>{t('volumeSnapExplainV2')}</p>

                    {/* Step 1 — pick a disk */}
                    {snapshotDisks(selectedSnapshot).map((d, i) => (
                      <div key={i} style={{display: 'flex', alignItems: 'center', gap: '10px', padding: '3px 0'}}>
                        <span className="mono" style={{fontSize: '12px'}}>💿 {String(d).replace('.img.fidx','')}</span>
                        <button className="btn" style={{padding: '2px 10px', fontSize: '12px'}} disabled={loadingParts} onClick={() => handleListPartitions(d)}>
                          {loadingParts ? '⏳ ' : '🔍 '}{t('volumeShowParts')}
                        </button>
                      </div>
                    ))}

                    {/* Step 2 — pick a partition. Every partition is listed,
                        browsable or not, with its filesystem and sizes, so the
                        user chooses instead of us guessing (we used to open the
                        first NTFS volume, which was the WinRE partition). */}
                    {imagePartitions && (
                      <div style={{marginTop: '12px'}}>
                        <p style={{margin: '0 0 6px', fontSize: '12px', fontWeight: 600}}>
                          {t('volumePartsOn')} <span className="mono">{String(imagePartitions.disk).replace('.img.fidx','')}</span>
                        </p>
                        <table className="nc-part-table">
                          <thead>
                            <tr>
                              <th>#</th>
                              <th>{t('partName')}</th>
                              <th>{t('partType')}</th>
                              <th>{t('partFs')}</th>
                              <th style={{textAlign: 'right'}}>{t('partUsed')}</th>
                              <th style={{textAlign: 'right'}}>{t('partAllocated')}</th>
                              <th></th>
                            </tr>
                          </thead>
                          <tbody>
                            {imagePartitions.rows.map(p => (
                              <tr key={p.index} className={p.index === imagePartIndex ? 'selected' : ''}>
                                <td>{p.index}</td>
                                <td>{p.volume_label || p.name || '—'}</td>
                                <td>{p.type}</td>
                                <td className="mono">{p.filesystem}</td>
                                <td style={{textAlign: 'right'}}>{p.used_known ? formatBytes(p.used_bytes) : '—'}</td>
                                <td style={{textAlign: 'right'}}>{formatBytes(p.allocated_bytes)}</td>
                                <td style={{textAlign: 'right'}}>
                                  {p.browsable ? (
                                    <button className="btn btn-primary" style={{padding: '2px 10px', fontSize: '12px'}}
                                      disabled={browsingImage}
                                      onClick={() => handleBrowseImageDisk(imagePartitions.disk, p.index)}>
                                      {browsingImage ? '⏳ ' : '📂 '}{t('volumeBrowseFiles')}
                                    </button>
                                  ) : (
                                    <span className="hint-text" title={p.reason}>{p.reason}</span>
                                  )}
                                </td>
                              </tr>
                            ))}
                          </tbody>
                        </table>
                      </div>
                    )}

                    <p style={{margin: '10px 0 0', fontSize: '12px', color: 'var(--nc-text-dim)'}}>{t('volumeSnapRestoreHint')}</p>
                  </div>
                ) : snapshotEntries.length === 0 ? (
                  <p style={{padding: '12px', color: 'var(--nc-text-dim)'}}>{t('loadingOrEmpty')}</p>
                ) : (
                  <>
                    {imageDisk && (
                      <div style={{padding: '6px 10px', borderBottom: '1px solid var(--nc-border-soft)', fontSize: '12px', display: 'flex', gap: '8px', alignItems: 'center', flexWrap: 'wrap'}}>
                        <span style={{color: 'var(--nc-accent)', fontWeight: 600}}>💿 {String(imageDisk).replace('.img.fidx','')} · {t('partition')} {imagePartIndex}</span>
                        <span className="hint-text">{t('volumeBrowsingBadge')}</span>
                        <button className="btn" style={{padding: '1px 8px', fontSize: '11px'}}
                          onClick={() => { setSnapshotEntries([]); setSelectedPaths(new Set()); setImageDisk(null) }}>
                          ↩ {t('volumeBackToParts')}
                        </button>
                        {imageTruncated && <span style={{color: 'var(--nc-warn)'}}>⚠ {t('volumeTreeTruncated')}</span>}
                      </div>
                    )}
                    {(() => {
                      const tree = buildTree(snapshotEntries)
                      const roots = tree.get('') || []
                      return roots.map(e => renderTreeNode(e, tree, 0))
                    })()}
                  </>
                )}
              </div>
              <p style={{marginTop: '6px', fontSize: '12px', color: 'var(--nc-text-dim)'}}>
                {selectedPaths.size === 0
                  ? t('selectionEmptyAllSize').replace('{size}', formatBytes(selectionBytes))
                  : t('selectionCountSize')
                      .replace('{n}', selectedPaths.size)
                      .replace('{size}', formatBytes(selectionBytes))}
              </p>
              <div style={{marginTop: '8px', display: 'flex', gap: '10px', alignItems: 'center', flexWrap: 'wrap'}}>
                <button
                  className="btn btn-primary"
                  disabled={selectedPaths.size === 0 || downloading}
                  onClick={handleDownloadSelection}
                  title={t('downloadHint')}
                >
                  ⬇️ {downloading ? t('downloadInProgress') : t('downloadSelection')}
                </button>
                <span className="hint-text">{t('downloadModesHint')}</span>
              </div>
            </div>
          )}

          {/* Restore mode picker + destination + options + restore button */}
          {selectedSnapshot && (() => {
            const blocker = inPlaceBlocker()
            const isInPlace = restoreMode === 'original'
            const crossHost = isInPlace && crossHostMismatch()
            const blockerTooltip = blocker ? (t(blocker) || '') : ''
            return (
              <div style={{marginTop: '20px'}}>
                {/* Mode picker */}
                <div style={{
                  display: 'flex',
                  flexWrap: 'wrap',
                  gap: '20px',
                  marginBottom: '14px',
                  padding: '10px 14px',
                  border: '1px solid var(--nc-border)',
                  borderRadius: '8px',
                  backgroundColor: 'var(--nc-panel-head)'
                }}>
                  <label
                    title={blockerTooltip}
                    style={{
                      display: 'flex', alignItems: 'center', gap: '6px',
                      opacity: blocker ? 0.5 : 1,
                      cursor: blocker ? 'not-allowed' : 'pointer'
                    }}
                  >
                    <input
                      type="radio"
                      name="restoreMode"
                      value="original"
                      checked={restoreMode === 'original'}
                      disabled={!!blocker}
                      onChange={() => setRestoreMode('original')}
                    />
                    <strong>{t('restoreModeInPlace') || 'Restaurer in-place'}</strong>
                    {blocker && <span style={{fontSize: '11px', color: 'var(--nc-err)'}}> ({blockerTooltip})</span>}
                  </label>
                  <label style={{display: 'flex', alignItems: 'center', gap: '6px', cursor: 'pointer'}}>
                    <input
                      type="radio"
                      name="restoreMode"
                      value="alternate"
                      checked={restoreMode !== 'original'}
                      onChange={() => setRestoreMode('alternate_abs')}
                    />
                    <strong>{t('restoreModeAlternate') || 'Restaurer vers un autre emplacement'}</strong>
                  </label>
                </div>

                {/* In-place: warning banner + cross-host override */}
                {isInPlace && (
                  <div style={{
                    marginBottom: '14px',
                    padding: '10px 14px',
                    border: '1px solid color-mix(in srgb, var(--nc-err) 45%, var(--nc-border))',
                    backgroundColor: 'var(--nc-err-bg)',
                    borderRadius: '8px',
                    fontSize: '13px'
                  }}>
                    <div style={{color: 'var(--nc-err)', marginBottom: '6px'}}>
                      ⚠️ {t('inPlaceWarning').replace('{path}', snapshotMeta?.original_path || '?')}
                    </div>
                    {crossHost && (
                      <label style={{display: 'flex', alignItems: 'center', gap: '6px', color: 'var(--nc-text)'}}>
                        <input
                          type="checkbox"
                          checked={restoreAllowCrossHost}
                          onChange={(e) => setRestoreAllowCrossHost(e.target.checked)}
                        />
                        {t('crossHostOverride')
                          .replace('{src}', snapshotMeta?.hostname || '?')
                          .replace('{dst}', hostname || '?')}
                      </label>
                    )}
                  </div>
                )}

                {/* Alternate: destination + keep-tree toggle */}
                {!isInPlace && (
                  <>
                    <div className="form-group">
                      <label>{t('destinationPath')}</label>
                      <div style={{display: 'flex', gap: '8px'}}>
                        <input
                          type="text"
                          value={restoreDestPath}
                          onChange={(e) => setRestoreDestPath(e.target.value)}
                          placeholder="C:\Restore"
                          style={{flex: 1}}
                        />
                        <button className="btn" onClick={handleBrowseRestoreDest} type="button">
                          📁 {t('browse')}
                        </button>
                      </div>
                    </div>
                    <label style={{display: 'flex', alignItems: 'center', gap: '6px', marginBottom: '12px'}}>
                      <input
                        type="checkbox"
                        checked={restoreKeepTree}
                        onChange={(e) => setRestoreKeepTree(e.target.checked)}
                      />
                      {t('keepTreeLabel') || 'Conserver l\'arborescence d\'origine'}
                      <span style={{fontSize: '12px', color: 'var(--nc-text-dim)'}}>
                        {restoreKeepTree
                          ? (t('keepTreeOnHint') || '(dest/Users/alice/doc.txt)')
                          : (t('keepTreeOffHint') || '(dest/doc.txt — recommandé pour un fichier seul)')}
                      </span>
                    </label>
                  </>
                )}

                <div style={{display: 'flex', flexWrap: 'wrap', gap: '12px', marginBottom: '12px'}}>
                  <label style={{display: 'flex', alignItems: 'center', gap: '6px', opacity: isInPlace ? 0.5 : 1}}
                         title={isInPlace ? (t('overwriteForcedInPlace') || '') : ''}>
                    <input
                      type="checkbox"
                      checked={isInPlace ? true : restoreOptions.overwrite}
                      disabled={isInPlace}
                      onChange={(e) => setRestoreOptions(o => ({...o, overwrite: e.target.checked}))}
                    />
                    {t('optionOverwrite')}
                  </label>
                  <label style={{display: 'flex', alignItems: 'center', gap: '6px'}}>
                    <input
                      type="checkbox"
                      checked={restoreOptions.timestamps}
                      onChange={(e) => setRestoreOptions(o => ({...o, timestamps: e.target.checked}))}
                    />
                    {t('optionTimestamps')}
                  </label>
                  <label style={{display: 'flex', alignItems: 'center', gap: '6px', opacity: 0.5}} title={t('optionComingSoon')}>
                    <input type="checkbox" disabled checked={false} />
                    {t('optionACLs')} <span style={{fontSize: '11px'}}>({t('comingSoon')})</span>
                  </label>
                  <label style={{display: 'flex', alignItems: 'center', gap: '6px', opacity: 0.5}} title={t('optionComingSoon')}>
                    <input type="checkbox" disabled checked={false} />
                    {t('optionADS')} <span style={{fontSize: '11px'}}>({t('comingSoon')})</span>
                  </label>
                </div>

                <button
                  className="btn btn-primary"
                  onClick={handleRestoreSnapshot}
                  disabled={restoreLoading || (isInPlace && crossHost && !restoreAllowCrossHost)}
                >
                  {restoreLoading ? `⏳ ${t('restoring')}` : `▶️ ${t('restore')}`}
                </button>

                {restoreLoading && (
                  <div style={{marginTop: '12px'}}>
                    <div style={{height: '8px', backgroundColor: 'var(--nc-border-soft)', borderRadius: '4px', overflow: 'hidden'}}>
                      <div style={{
                        height: '100%',
                        width: `${restoreProgress}%`,
                        backgroundColor: 'var(--nc-accent)',
                        transition: 'width 0.3s ease'
                      }}/>
                    </div>
                    <p style={{textAlign: 'center', fontSize: '13px', color: 'var(--nc-text-dim)', marginTop: '4px'}}>
                      {restoreProgress}%
                    </p>
                  </div>
                )}
              </div>
            )
          })()}

          <div className="info-box" style={{marginTop: '20px'}}>
            💡 <strong>{t('restoreInfo')}</strong> {t('restoreInfoText')}<br/>
            {t('restoreInfoText2')}
          </div>

          {status.visible && activeTab === 'restore' && (
            <div className={`status ${status.type} visible`}>{status.message}</div>
          )}
        </div>

        {/* About Tab */}
        <div className={`tab-content ${activeTab === 'about' ? 'active' : ''}`}>
          <h2 style={{textAlign: 'center'}}>{t('aboutTitle')}</h2>

          <img
            src="https://nimbus.rdem-systems.com/logo.webp"
            alt="Nimbus Backup"
            className="logo"
            onError={(e) => e.target.style.display = 'none'}
          />

          <div style={{textAlign: 'center', marginTop: '30px'}}>
            <h3>Nimbus Backup</h3>
            <p style={{color: 'var(--nc-text-dim)', margin: '10px 0'}}>{t('version')} {appVersion}</p>

            {/* Upsell CTA */}
            <div style={{margin: '20px 0'}}>
              <a
                href={`${t('chooseBackupUrl')}?utm_source=NimbusGui&utm_medium=tooling&utm_campaign=version-${appVersion}&utm_content=version-${appVersion}`}
                target="_blank"
                rel="noopener noreferrer"
                style={{
                  display: 'inline-block',
                  padding: '12px 24px',
                  backgroundColor: 'var(--nc-accent)',
                  color: 'var(--nc-accent-text)',
                  textDecoration: 'none',
                  borderRadius: 'var(--nc-radius)',
                  fontWeight: 'bold',
                  transition: 'filter 0.2s'
                }}
                onMouseEnter={(e) => e.target.style.filter = 'brightness(1.1)'}
                onMouseLeave={(e) => e.target.style.filter = ''}
              >
                📦 {t('orderStorageCTA')}
              </a>
            </div>

            <div className="grid" style={{marginTop: '30px', textAlign: 'left'}}>
              <div className="card">
                <h3>✅ {t('features')}</h3>
                <ul style={{lineHeight: 2, marginLeft: '20px'}}>
                  <li>{t('featuresList.directories')}</li>
                  <li>{t('featuresList.machine')}</li>
                  <li>{t('featuresList.restore')}</li>
                  <li>{t('featuresList.vss')}</li>
                  <li>{t('featuresList.dedup')}</li>
                  <li>{t('featuresList.modern')}</li>
                </ul>
              </div>

              <div className="card">
                <h3>🚀 {t('technology')}</h3>
                <ul style={{lineHeight: 2, marginLeft: '20px'}}>
                  <li>{t('techList.wails')}</li>
                  <li>{t('techList.performance')}</li>
                  <li>{t('techList.interface')}</li>
                  <li>{t('techList.logs')}</li>
                  <li>{t('techList.nogpu')}</li>
                </ul>
              </div>
            </div>

            <p style={{marginTop: '30px'}}>
              <strong>{t('copyright')}</strong><br/>
              <a href="https://nimbus.rdem-systems.com" style={{color: 'var(--nc-accent)'}}>nimbus.rdem-systems.com</a>
            </p>

            <p style={{marginTop: '20px', color: 'var(--nc-text-dim)', fontSize: '12px'}}>
              {t('basedOn')}<br/>
              {t('techStack')}
            </p>
          </div>
        </div>
      </div>
    </>
  )
}

export default App
