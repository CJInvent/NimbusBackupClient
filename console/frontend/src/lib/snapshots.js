// What a snapshot is, and the derived values the restore screen draws from
// it. Pure functions: no state, no bindings.

// Volume (disk-image) backup detection: machine backups store one
// *.img.fidx per disk (fixed-index raw images) instead of a pxar archive.
export const isVolumeSnapshot = (snap) => {
  if (!snap) return false
  const files = snap.files || []
  if (files.some(f => String(f).includes('.img.fidx'))) return true
  return snap.backup_type === 'vm' || snap.backup_type === 'machine'
}

export const snapshotDisks = (snap) => (snap && snap.files ? snap.files.filter(f => String(f).includes('.img.fidx')) : [])

// Build a map childrenByDir: dir -> [entry...] from the flat list, plus a
// set of all dir paths. Re-derived on every render — entries are tiny.
export const buildTree = (entries) => {
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

// Flatten the visible tree (respecting expanded state) in display order —
// the range that shift+click selects, exactly like a file manager.
export const visibleOrder = (entries, expandedDirs) => {
  const tree = buildTree(entries)
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

// Reconstruct the absolute on-disk location a file came from, by joining the
// backup's original_path (from the meta sidecar) with the archive-relative
// path. Critical for multi-folder backups, where each folder is a separate backup-id
// and the relative tree alone doesn't tell you which physical folder/drive a
// file belonged to. Returns null when the snapshot has no meta sidecar.
export const absOriginPath = (snapshotMeta, archivePath) => {
  const root = snapshotMeta?.original_path
  if (!root) return null
  const sep = (snapshotMeta?.os === 'windows' || root.includes('\\')) ? '\\' : '/'
  const base = root.endsWith(sep) ? root.slice(0, -1) : root
  if (!archivePath) return base
  return base + sep + archivePath.split('/').join(sep)
}

// inPlaceBlocker returns a translation key (or null when in-place is OK).
// Drives the disabled state of the in-place radio + its tooltip.
export const inPlaceBlocker = (snapshotMeta, systemInfo) => {
  if (!snapshotMeta) return 'inPlaceNoMeta'
  if (!snapshotMeta.original_path) return 'inPlaceNoOriginalPath'
  if (systemInfo.os && snapshotMeta.os && systemInfo.os !== snapshotMeta.os) return 'inPlaceOsMismatch'
  return null
}

// crossHostMismatch returns true when the backup hostname differs from the
// current machine. Comparison is case-insensitive and ignores the domain
// suffix — same rule as backend equalHostnames.
export const crossHostMismatch = (snapshotMeta, hostname) => {
  if (!snapshotMeta || !snapshotMeta.hostname || !hostname) return false
  const norm = s => (s || '').toLowerCase().split('.')[0]
  return norm(snapshotMeta.hostname) !== norm(hostname)
}
