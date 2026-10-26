// One row per screen component and state: [name, module, props, language].
import * as F from './fixtures.mjs'

const S = '/src/screens'
const status = { message: 'Saved', type: 'success', visible: true }
const hidden = { message: '', type: '', visible: false }
const srv = (over = {}) => F.withHandlers({ pbsServers: F.servers, defaultPBSID: 'pbs1', editingServer: null, serverFormData: F.emptyServer, serverStatus: { pbs1: 'online', pbs2: 'offline' }, ...over })
const cp = { cpStatus: F.cpStatus, cpForm: { url: 'https://nimbus.example.com', token: '', fp: '' }, setCpForm: F.noop, saveControlServer: F.noop }
const app = { config: F.config, setConfig: F.noop, hostname: 'DESKTOP-01' }
const security = { exchangeStatus: { installed: true, version: '15.2', highlight_setting: true }, exchangeLogMode: { queried: true, logs_accumulate: true, detail: 'circular logging is off' } }
const act = F.withHandlers({})
const runIdle = { progress: 0, backupStats: F.backupStats, backupPhase: '' }
const runBusy = { progress: 42, backupStats: F.backupStats, backupPhase: 'Reading C:' }
const jobs = { scheduledJobs: [F.job], jobHistory: F.history }
const volume = { selectedSnapshot: F.volumeSnap, snapshotEntries: [], snapshotMeta: null }
const image = { selectedSnapshot: F.volumeSnap, snapshotMeta: null, imageDisk: 'drive-sata0.img.fidx', imagePartitions: F.partitions, imagePartIndex: 2, imageCwd: '/Windows/System32', snapshotEntries: F.imgRows, selectedPaths: new Set(['/readme.txt']), imageSort: { key: 'size', dir: -1 } }

export const cases = [
  // frame
  ['Header', `${S}/frame/Header.jsx`, {}],
  ['OrgSuspendedBanner', `${S}/frame/OrgSuspendedBanner.jsx`, { cpStatus: { org_suspended: true } }],
  ['SecurityWarnings', `${S}/frame/SecurityWarnings.jsx`, { warnings: ['Windows Defender real-time protection is off'] }],
  ['Tabs', `${S}/frame/Tabs.jsx`, { activeTab: 'backup', onSelect: F.noop }],
  ['Tabs (fr)', `${S}/frame/Tabs.jsx`, { activeTab: 'servers', onSelect: F.noop }, 'fr'],
  ['ReleaseBanners update', `${S}/frame/ReleaseBanners.jsx`, { status: { update_pending: '1.01.04' } }],
  ['ReleaseBanners older line', `${S}/frame/ReleaseBanners.jsx`, { status: { agent_line: 'older', agent_line_needs: '1.02.00' } }],
  ['ReleaseBanners newer line (fr)', `${S}/frame/ReleaseBanners.jsx`, { status: { agent_line: 'newer', agent_line_needs: '1.00.07' } }, 'fr'],
  ['StatusNotice', `${S}/frame/StatusNotice.jsx`, { status, show: true }],

  // status page
  ['ConnectionTile ok', `${S}/status/ConnectionTile.jsx`, { title: 'Main', state: 'ok', lines: ['pbs.example', null, 'ds1'] }],
  ['ConnectionTile unknown', `${S}/status/ConnectionTile.jsx`, { title: 'Offsite', state: 'unknown', lines: ['never checked'] }],
  ['LiveRun', `${S}/status/LiveRun.jsx`, { run: F.run, serverTime: F.NOW }],
  ['LiveRun idle', `${S}/status/LiveRun.jsx`, { run: null }],
  ['RecentRuns', `${S}/status/RecentRuns.jsx`, { recent: F.recent }],
  ['StatusPanelView', `${S}/status/StatusPanelView.jsx`, { version: '1.01.03', hostname: 'DESKTOP-01', conns: F.conns, active: F.run, recent: F.recent, serviceUp: true }],
  ['StatusPanelView service down', `${S}/status/StatusPanelView.jsx`, { version: '1.01.03', hostname: 'DESKTOP-01', conns: null, active: null, recent: [], serviceUp: false }],
  ['LockedView', `${S}/status/LockedView.jsx`, { cpStatus: { org_suspended: true }, version: '1.01.03', hostname: 'DESKTOP-01' }],

  // servers
  ['ControlServerCard', `${S}/setup/ControlServerCard.jsx`, { cpStatus: { ...F.cpStatus, connected: false, last_error: 'dial tcp: timeout' }, cpForm: cp.cpForm, setCpForm: F.noop, onSave: F.noop }],
  ['ServerWelcome', `${S}/setup/ServerWelcome.jsx`, { config: { baseurl: '' }, appVersion: '1.01.03' }],
  ['ServerForm first', `${S}/setup/ServerForm.jsx`, { first: true, editingServer: null, serverFormData: F.emptyServer, setServerFormData: F.noop }],
  ['ServerForm editing', `${S}/setup/ServerForm.jsx`, { first: false, editingServer: 'pbs2', serverFormData: F.servers[1], setServerFormData: F.noop }],
  ['ServerTable', `${S}/setup/ServerTable.jsx`, { pbsServers: F.servers, defaultPBSID: 'pbs1', serverStatus: { pbs1: 'testing' } }],
  ['SetupScreen none', `${S}/setup/SetupScreen.jsx`, { active: true, srv: srv({ pbsServers: [], defaultPBSID: '' }), cp, config: { baseurl: '' }, appVersion: '1.01.03', status }],
  ['SetupScreen servers', `${S}/setup/SetupScreen.jsx`, { active: true, srv: srv(), cp, config: F.config, appVersion: '1.01.03', status: hidden }],

  // backup
  ['BackupTypeField', `${S}/backup/BackupTypeField.jsx`, { backupType: 'machine', setBackupType: F.noop }],
  ['BackupModeField', `${S}/backup/BackupModeField.jsx`, { backupMode: 'scheduled', setBackupMode: F.noop }],
  ['ScheduleOptions editing', `${S}/backup/ScheduleOptions.jsx`, { editingJobId: 'j1', scheduleTime: '03:30', runAtStartup: true }],
  ['DirectoryFields', `${S}/backup/DirectoryFields.jsx`, { backupDirs: 'C:\\Data\nD:\\Docs', excludeList: '*.tmp', config: F.config }],
  ['MachineDisks', `${S}/backup/MachineDisks.jsx`, { physicalDisks: F.disks, disksLoading: false, disksError: '', selectedDrives: ['boot'] }],
  ['MachineDisks loading', `${S}/backup/MachineDisks.jsx`, { physicalDisks: [], disksLoading: true, disksError: '', selectedDrives: [] }],
  ['MachineDisks error', `${S}/backup/MachineDisks.jsx`, { physicalDisks: [], disksLoading: false, disksError: 'access denied', selectedDrives: [] }],
  ['MachineDisks none', `${S}/backup/MachineDisks.jsx`, { physicalDisks: [], disksLoading: false, disksError: '', selectedDrives: [] }],
  ['BackupSettings', `${S}/backup/BackupSettings.jsx`, { config: F.config, setConfig: F.noop, ...security }],
  ['ExchangeOptions', `${S}/backup/ExchangeOptions.jsx`, { config: { exchange_aware: true }, setConfig: F.noop, ...security }],
  ['BackupProgressCard', `${S}/backup/BackupProgressCard.jsx`, { ...runBusy, status: { message: 'Stopping', type: 'info', visible: true } }],
  ['BackupButtons running', `${S}/backup/BackupButtons.jsx`, { backupMode: 'oneshot', progress: 42, editingJobId: null }],
  ['BackupButtons editing', `${S}/backup/BackupButtons.jsx`, { backupMode: 'scheduled', progress: 0, editingJobId: 'j1' }],
  ['BackupScreen', `${S}/backup/BackupScreen.jsx`, { active: true, f: F.form(), run: runBusy, jobs, app, security, act, status: hidden }],
  ['BackupScreen scheduled machine', `${S}/backup/BackupScreen.jsx`, { active: true, f: F.form({ backupType: 'machine', backupMode: 'scheduled', editingJobId: 'j1' }), run: runIdle, jobs, app, security: { exchangeStatus: { installed: false }, exchangeLogMode: {} }, act, status }],
  ['ScheduledJobs', `${S}/jobs/ScheduledJobs.jsx`, { scheduledJobs: [F.job] }],
  ['JobHistory', `${S}/history/JobHistory.jsx`, { jobHistory: F.history }],

  // restore
  ['RestoreBetaBox', `${S}/restore/RestoreBetaBox.jsx`, {}],
  ['RestoreSource', `${S}/restore/RestoreSource.jsx`, { r: F.restore(), pbsServers: F.servers, defaultPBSID: 'pbs1', hostname: 'DESKTOP-01' }],
  ['RestoreSource no server', `${S}/restore/RestoreSource.jsx`, { r: F.restore({ restoreBackupId: '' }), pbsServers: [], defaultPBSID: '', hostname: '' }],
  ['SearchPanel running', `${S}/restore/SearchPanel.jsx`, { r: F.restore({ searchRunning: true, searchProgress: { percent: 40, message: 'snapshot 2 of 5' } }), hostname: 'DESKTOP-01' }],
  ['SearchResults', `${S}/restore/SearchResults.jsx`, { r: F.restore({ searchResult: F.searchResult }) }],
  ['SearchResults none', `${S}/restore/SearchResults.jsx`, { r: F.restore({ searchResult: { hits: [], snapshots_searched: 3, snapshots_in_range: 0 } }) }],
  ['SnapshotGrid', `${S}/restore/SnapshotGrid.jsx`, { r: F.restore() }],
  ['SnapshotGrid empty', `${S}/restore/SnapshotGrid.jsx`, { r: F.restore({ snapshots: [] }) }],
  ['SnapshotMetaBanner', `${S}/restore/SnapshotMetaBanner.jsx`, { snapshotMeta: F.meta }],
  ['SnapshotContents file tree', `${S}/restore/SnapshotContents.jsx`, { r: F.restore() }],
  ['SnapshotContents volume', `${S}/restore/SnapshotContents.jsx`, { r: F.restore({ ...volume, imagePartitions: F.partitions, loadingParts: true }) }],
  ['SnapshotContents image folder', `${S}/restore/SnapshotContents.jsx`, { r: F.restore(image) }],
  ['SnapshotContents empty', `${S}/restore/SnapshotContents.jsx`, { r: F.restore({ snapshotEntries: [], selectedPaths: new Set(), selectionBytes: 0 }) }],
  ['VolumeDisks', `${S}/restore/VolumeDisks.jsx`, { r: F.restore(volume) }],
  ['PartitionTable', `${S}/restore/PartitionTable.jsx`, { r: F.restore({ ...volume, imagePartitions: F.partitions, imagePartIndex: 2, browsingImage: true }) }],
  ['ImageDirectory', `${S}/restore/ImageDirectory.jsx`, { r: F.restore(image) }],
  ['FileTree', `${S}/restore/FileTree.jsx`, { r: F.restore({ expandedDirs: new Set(['Users', 'Users/alice']) }) }],
  ['RestoreModePicker blocked', `${S}/restore/RestoreModePicker.jsx`, { r: F.restore(), blocker: 'inPlaceNoMeta', blockerTooltip: 'No metadata' }],
  ['InPlaceWarning cross-host', `${S}/restore/InPlaceWarning.jsx`, { r: F.restore({ restoreMode: 'original' }), crossHost: true, hostname: 'DESKTOP-01' }],
  ['RestoreDestination', `${S}/restore/RestoreDestination.jsx`, { r: F.restore({ restoreKeepTree: true }) }],
  ['RestoreOptions', `${S}/restore/RestoreOptions.jsx`, { r: F.restore(), isInPlace: false }],
  ['RestoreOptions zip in place', `${S}/restore/RestoreOptions.jsx`, { r: F.restore({ packageAsZip: true, restoreMatrix: F.matrix(true) }), isInPlace: true }],
  ['RestoreButtons busy', `${S}/restore/RestoreButtons.jsx`, { r: F.restore({ restoreLoading: true }), isInPlace: false, crossHost: false }],
  ['TaskProgress', `${S}/restore/TaskProgress.jsx`, { taskProg: { pct: 37, msg: 'Restoring report.docx', done: 4e8, total: 1.1e9, bps: 2.5e7, eta: 95, indet: false } }],
  ['TaskProgress indeterminate', `${S}/restore/TaskProgress.jsx`, { taskProg: { pct: 0, msg: 'Planning', done: 0, total: 0, bps: 0, eta: null, indet: true } }],
  ['RestoreForm in place', `${S}/restore/RestoreForm.jsx`, { r: F.restore({ restoreMode: 'original', taskProg: { pct: 100, msg: 'Done', done: 1, total: 1, bps: 0, eta: 0, indet: false } }), hostname: 'DESKTOP-01', systemInfo: { os: 'windows' } }],
  ['RestoreForm alternate', `${S}/restore/RestoreForm.jsx`, { r: F.restore({ snapshotMeta: null }), hostname: 'DESKTOP-01', systemInfo: { os: 'windows' } }],
  ['RestoreScreen', `${S}/restore/RestoreScreen.jsx`, { active: true, r: F.restore({ showSearch: true, searchResult: F.searchResult }), pbsServers: F.servers, defaultPBSID: 'pbs1', hostname: 'DESKTOP-01', systemInfo: { os: 'windows' }, status }],
  ['RestoreScreen (fr)', `${S}/restore/RestoreScreen.jsx`, { active: true, r: F.restore({ selectedSnapshot: null, showSnapshots: false }), pbsServers: F.servers, defaultPBSID: 'pbs1', hostname: 'DESKTOP-01', systemInfo: { os: 'windows' }, status: hidden }, 'fr'],

  // about
  ['AboutLists', `${S}/about/AboutLists.jsx`, {}],
  ['AboutScreen', `${S}/about/AboutScreen.jsx`, { active: true, appVersion: '1.01.03' }],
  ['AboutScreen (es)', `${S}/about/AboutScreen.jsx`, { active: true, appVersion: '1.01.03' }, 'es'],
]
