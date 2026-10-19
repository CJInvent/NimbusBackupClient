// The Wails bindings the GUI calls: methods of the Go `Console` the desktop
// shell injects as window.go.main.Console, and the runtime's event bus. Read
// once, at load; in a plain browser (vite dev) every name is undefined and
// each caller says "the runtime is not available".
//
// THIS FILE IS THE ONE COMPLETE LIST. No other file reads window.go (the
// render tests check it), and gui/console_bindings_test.go pins this list
// against the Console's methods in both directions: every name here is bound,
// and nothing is bound that is not here. Each binding is one call to the
// service (docs/V4-CONSOLE.md section 3).
//
// Only hooks and their action files import this module. A component renders
// the props it is given and never calls the service (docs/V4-GUI-AUDIT.md
// section 4.11).

const app = typeof window !== 'undefined' && window.go && window.go.main ? (window.go.main.Console || {}) : {}

// Configuration, version, machine
export const GetConfigWithHostname = app.GetConfigWithHostname
export const GetVersion = app.GetVersion
export const GetSystemInfo = app.GetSystemInfo
export const GetLastBackupDirs = app.GetLastBackupDirs
export const GetSecurityWarnings = app.GetSecurityWarnings
export const GetExchangeStatus = app.GetExchangeStatus
export const QueryExchangeLogMode = app.QueryExchangeLogMode
export const ListPhysicalDisks = app.ListPhysicalDisks

// Control server, the lock, the storage identity, the tray
export const GetControlServerStatus = app.GetControlServerStatus
export const SaveControlServerConfig = app.SaveControlServerConfig
export const IsReadOnly = app.IsReadOnly
export const GetStorageIdentityStatus = app.GetStorageIdentityStatus
export const ApproveStorageIdentity = app.ApproveStorageIdentity
export const SetTrayLanguage = app.SetTrayLanguage

// Backups and schedules; the run registry (what is running, whatever started
// it, and the history, which the registry keeps across restarts)
export const StartBackup = app.StartBackup
export const StopBackup = app.StopBackup
export const SaveScheduledJob = app.SaveScheduledJob
export const UpdateScheduledJob = app.UpdateScheduledJob
export const GetScheduledJobs = app.GetScheduledJobs
export const DeleteScheduledJob = app.DeleteScheduledJob
export const GetActiveRuns = app.GetActiveRuns
export const GetRecentRuns = app.GetRecentRuns
export const GetConnections = app.GetConnections

// Restore: snapshots, file backups, disk images, search, downloads
export const ListSnapshots = app.ListSnapshots
export const ListSnapshotContents = app.ListSnapshotContents
export const GetSnapshotMeta = app.GetSnapshotMeta
export const RestoreSnapshot = app.RestoreSnapshot
export const SearchFiles = app.SearchFiles
export const CancelSearch = app.CancelSearch
export const CheckDownloadSpace = app.CheckDownloadSpace
export const DownloadSelection = app.DownloadSelection
export const ListVolumeFiles = app.ListVolumeFiles
export const LastVolumeListTruncated = app.LastVolumeListTruncated
export const ListVolumePartitions = app.ListVolumePartitions
export const ListVolumeDirectory = app.ListVolumeDirectory
export const RestoreFilesFromVolume = app.RestoreFilesFromVolume
export const DownloadFilesFromVolume = app.DownloadFilesFromVolume
export const CancelVolumeFileRestore = app.CancelVolumeFileRestore

// The in-app path picker (replaces the native dialogs, which crashed the
// process)
export const ListDrives = app.ListDrives
export const ListFolders = app.ListFolders
export const CreateFolder = app.CreateFolder
export const DefaultSaveDir = app.DefaultSaveDir

// Storage servers (standalone)
export const ListPBSServers = app.ListPBSServers
export const AddPBSServer = app.AddPBSServer
export const UpdatePBSServer = app.UpdatePBSServer
export const DeletePBSServer = app.DeletePBSServer
export const SetDefaultPBSServer = app.SetDefaultPBSServer
export const GetDefaultPBSID = app.GetDefaultPBSID
export const TestPBSConnection = app.TestPBSConnection
export const GetServerFingerprint = app.GetServerFingerprint
export const PinPBSServerFingerprint = app.PinPBSServerFingerprint

// Wails events
export const EventsOn = typeof window !== 'undefined' && window.runtime ? window.runtime.EventsOn : undefined
