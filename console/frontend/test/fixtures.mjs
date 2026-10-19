// Fixture state for the render tests: plain data, and no-op handlers. A
// model built with withHandlers() answers any setX, handleX or toggleX with
// a no-op, so a fixture names only the state it is about.

export const noop = () => {}
export const withHandlers = (data) => new Proxy(data, {
  get: (o, k) => (k in o ? o[k] : (typeof k === 'string' && /^(set|handle|toggle|on)[A-Z]/.test(k) ? noop : undefined)),
})

export const NOW = '2026-10-16T17:00:00Z'

export const cpStatus = { configured: true, connected: true, enrolled: true, agent_id: 12, server_host: 'nimbus.example.com', last_checkin: NOW }
export const servers = [
  { id: 'pbs1', name: 'Main', baseurl: 'https://pbs.example:8007', datastore: 'ds1', namespace: 'acme', description: 'Primary' },
  { id: 'pbs2', name: 'Offsite', baseurl: 'https://pbs2.example:8007', datastore: 'ds2', namespace: '', secret_set: true },
]
export const emptyServer = { id: '', name: '', baseurl: '', certfingerprint: '', authid: '', secret: '', datastore: '', namespace: '', description: '' }
export const config = { baseurl: 'https://pbs.example:8007', 'backup-id': 'DESKTOP-01', usevss: true, upload_limit_mbps: 0 }

export const run = { run_id: 'r-9', job_name: 'Nightly', trigger: 'schedule', started_at: '2026-10-16T16:50:00Z', percent: 42.4, message: 'Reading C:', bytes_done: 50e9, bytes_total: 120e9, new_chunks: 1200, reused_chunks: 8800, current_dir: 'C:\\Users\\alice' }
export const recent = [
  { run_id: 'r-1', job_name: 'Nightly', trigger: 'schedule', started_at: '2026-10-15T07:00:00Z', ended_at: '2026-10-15T07:40:00Z', state: 'done', success: true },
  { run_id: 'r-2', job_name: 'Nightly', trigger: 'schedule', started_at: '2026-10-14T07:00:00Z', ended_at: '2026-10-14T07:05:00Z', state: 'done', success: false, error: 'PBS unreachable' },
  { run_id: 'r-3', backup_id: 'DESKTOP-01', trigger: 'gui', started_at: '2026-10-16T16:50:00Z', state: 'running' },
]
export const conns = { server_time: NOW, control_plane: { configured: true, connected: true, server_host: 'nimbus.example.com', last_success: NOW }, pbs: [{ id: 'p1', name: 'Main', host: 'pbs.example', datastore: 'ds1', reachable: true, checked_at: NOW }, { id: 'p2', name: 'Offsite', host: 'pbs2.example', datastore: 'ds2', reachable: null }] }

export const job = { id: 'j1', name: 'Backup DESKTOP-01', scheduleTime: '02:00', runAtStartup: true, backupDirs: ['C:\\Data'], diskTargets: [], backupId: 'DESKTOP-01', useVSS: true, backupType: 'directory', excludeList: ['*.tmp'] }
export const history = [
  { id: 'h1', name: 'Backup DESKTOP-01', status: 'success', timestamp: '2026-10-15T07:40:00Z', message: 'Backup complete', backupDirs: ['C:\\Data'] },
  { id: 'h2', name: 'Backup DESKTOP-01', status: 'failed', timestamp: '2026-10-14T07:05:00Z', message: '[NB-1001] base :: detail here', backupDirs: ['C:\\Data'] },
  { id: 'h3', name: 'Backup DESKTOP-01', status: 'running', timestamp: '2026-10-16T16:50:00Z', message: '', backupDirs: ['C:\\Data'] },
]
export const disks = [{ path: '\\\\.\\PhysicalDrive0', target: 'boot', sizeText: '1.0 TB' }, { path: '\\\\.\\PhysicalDrive1', target: 'device:abc', sizeText: '2.0 TB' }]
export const backupStats = { startTime: Date.parse('2026-10-16T16:50:00Z'), eta: 1400, byteRate: 83e6, bytesDone: 50e9, bytesTotal: 120e9, newChunks: 1200, reusedChunks: 8800, failedChunks: 3, currentDir: 'C:\\Users\\alice' }

export const form = (over = {}) => withHandlers({
  backupType: 'directory', backupDirs: 'C:\\Data', selectedDrives: ['boot'], physicalDisks: disks,
  disksLoading: false, disksError: '', excludeList: '*.tmp', backupMode: 'oneshot',
  scheduleTime: '02:00', runAtStartup: false, editingJobId: null, ...over,
})

export const fileSnap = { id: '2026-10-15T07:00:00Z', backup_id: 'DESKTOP-01', unix: 1792047600, time: '10/15/2026, 2:00:00 AM', backup_type: 'host', files: ['root.pxar.didx'] }
export const volumeSnap = { id: '2026-10-14T07:00:00Z', backup_id: 'DESKTOP-01', unix: 1791961200, time: '10/14/2026, 2:00:00 AM', backup_type: 'vm', files: ['drive-sata0.img.fidx', 'drive-sata1.img.fidx'] }
export const entries = [
  { path: 'Users', is_dir: true, size: 0 },
  { path: 'Users/alice', is_dir: true, size: 0 },
  { path: 'Users/alice/report.docx', is_dir: false, size: 1288410 },
  { path: 'boot.ini', is_dir: false, size: 211 },
]
export const imgRows = [
  { path: '/Windows', is_dir: true, size: 9000000, mtime: 1789000000 },
  { path: '/pagefile.sys', is_dir: false, size: 4000000000, mtime: 1789200000 },
  { path: '/readme.txt', is_dir: false, size: 120, mtime: 1788000000 },
]
export const partitions = { disk: 'drive-sata0.img.fidx', rows: [
  { index: 1, name: 'EFI', type: 'EFI System', filesystem: 'fat32', used_known: true, used_bytes: 33554432, allocated_bytes: 104857600, browsable: true },
  { index: 2, volume_label: 'Windows', type: 'Basic data', filesystem: 'ntfs', used_known: true, used_bytes: 120e9, allocated_bytes: 500e9, browsable: true, file_table_bytes: 1048576, file_table_frags: 3 },
  { index: 3, name: 'Recovery', type: 'Recovery', filesystem: 'ntfs', used_known: false, allocated_bytes: 600e6, browsable: false, reason: 'BitLocker-encrypted' },
] }
export const meta = { original_path: 'C:\\', hostname: 'LAPTOP-9', os: 'windows', vss_used: true, backup_time: '2026-10-15T07:00:00Z', client_version: '1.01.02' }
export const matrix = (zip = false) => zip
  ? { zip: { on: true, why: '' }, mtime: { on: false, why: 'ZIP' }, acl: { on: false, why: 'ZIP' }, ads: { on: false, why: 'ZIP' } }
  : { zip: { on: true, why: '' }, mtime: { on: true, why: '' }, acl: { on: false, why: 'sidecar' }, ads: { on: false, why: 'sidecar' } }
export const searchResult = { hits: [
  { backup_id: 'DESKTOP-01', snapshot_time: 1792047600, path: 'Users/alice/report.docx', origin_path: 'C:\\Users\\alice\\report.docx', is_dir: false, size: 1288410 },
  { backup_id: 'DESKTOP-01', snapshot_time: 1791961200, path: 'Users/alice', is_dir: true },
], snapshots_searched: 2, snapshots_assembled: 0, snapshots_in_range: 2, snapshots_skipped: 1, truncated: true, cancelled: false }

export const restore = (over = {}) => withHandlers({
  snapshots: [fileSnap, volumeSnap], restoreBackupId: 'DESKTOP-01', showSnapshots: true, restorePBSID: 'pbs1',
  selectedSnapshot: fileSnap, snapshotMeta: meta, snapshotEntries: entries, imageDisk: null, imageTruncated: false,
  browsingImage: false, imagePartitions: null, imagePartIndex: 0, loadingParts: false, imageCwd: '/',
  imageSort: { key: 'name', dir: 1 }, selectedInfo: new Map(), packageAsZip: false,
  expandedDirs: new Set(['Users']), selectedPaths: new Set(['Users/alice/report.docx']), lastClickedPath: null,
  restoreDestPath: 'C:\\Restore', restoreMode: 'alternate_abs', restoreAllowCrossHost: false, restoreKeepTree: false,
  restoreOptions: { overwrite: false, timestamps: true, acls: false, ads: false }, restoreLoading: false,
  downloading: false, taskProg: null, showSearch: false, searchQuery: 'report', searchMode: 'name', searchFrom: '',
  searchTo: '', searchAssembleMissing: false, searchRunning: false, searchProgress: { percent: 0, message: '' },
  searchResult: null, selectionBytes: 1288410, restoreMatrix: matrix(), entrySize: (e) => e.size || 0, ...over,
})
