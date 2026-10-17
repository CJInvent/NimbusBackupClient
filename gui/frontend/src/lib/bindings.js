// The Wails bindings the GUI calls: methods of the Go `App` the desktop shell
// injects as window.go.main.App, and the runtime's event bus. Read once, at
// load, as App.jsx always did; in a plain browser (vite dev) every name is
// undefined and each caller says "the runtime is not available".
//
// Only hooks and their action files import this module. A component renders
// the props it is given and never calls the service (docs/V4-GUI-AUDIT.md
// section 4.11).

const app = typeof window !== 'undefined' && window.go ? window.go.main.App : {}

// Configuration, version, machine
export const GetConfigWithHostname = app.GetConfigWithHostname
export const GetVersion = app.GetVersion
export const GetSystemInfo = app.GetSystemInfo
export const GetLastBackupDirs = app.GetLastBackupDirs
export const GetSecurityWarnings = app.GetSecurityWarnings
export const GetExchangeStatus = app.GetExchangeStatus
export const QueryExchangeLogMode = app.QueryExchangeLogMode
export const ListPhysicalDisks = app.ListPhysicalDisks

// Control server
export const GetControlServerStatus = app.GetControlServerStatus
export const SaveControlServerConfig = app.SaveControlServerConfig

// Backups, schedules, history; the run registry (what is running, whatever
// started it)
export const StartBackup = app.StartBackup
export const StopBackup = app.StopBackup
export const SaveScheduledJob = app.SaveScheduledJob
export const UpdateScheduledJob = app.UpdateScheduledJob
export const GetScheduledJobs = app.GetScheduledJobs
export const DeleteScheduledJob = app.DeleteScheduledJob
export const GetJobHistory = app.GetJobHistory
export const GetActiveRuns = app.GetActiveRuns
export const GetRecentRuns = app.GetRecentRuns

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
