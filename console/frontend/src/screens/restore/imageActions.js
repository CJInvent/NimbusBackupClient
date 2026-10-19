import { ListVolumePartitions, ListVolumeFiles, LastVolumeListTruncated, ListVolumeDirectory } from '../../lib/bindings'

// Browsing a disk image: its partitions, then one partition's folders.
export default function makeImageActions(r, { showStatus, t }) {
  // Step 1: list the disk's partitions. We do NOT auto-open one — picking for
  // the user dropped them inside the WinRE recovery volume, which looked like
  // a bug because it was one.
  const handleListPartitions = async (disk) => {
    if (!ListVolumePartitions || !r.selectedSnapshot) return
    r.setLoadingParts(true)
    r.setImagePartitions(null)
    r.setImagePartIndex(0)
    r.setSnapshotEntries([])
    r.setSelectedPaths(new Set())
    r.setImageDisk(null)
    showStatus(`💿 ${t('volumeReadingParts')}`, 'info')
    try {
      const parts = await ListVolumePartitions(
        r.restorePBSID || '',
        r.selectedSnapshot.backup_id || r.restoreBackupId,
        r.selectedSnapshot.id,
        r.selectedSnapshot.backup_type || 'vm',
        disk
      )
      r.setImagePartitions({ disk, rows: parts || [] })
      showStatus(`✅ ${(parts || []).length} ${t('volumePartsFound')}`, 'success')
    } catch (err) {
      const msg = '❌ ' + (err && err.message ? err.message : String(err))
      showStatus(msg, 'error')
      try { window.alert(msg) } catch (e) { /* no-op */ }
    } finally {
      r.setLoadingParts(false)
    }
  }

  // Step 2: open the partition the user chose.
  const handleBrowseImageDisk = async (disk, partIndex, forceRefresh = false) => {
    if (!r.selectedSnapshot) {
      showStatus('❌ ' + t('errNoSnapshotSelected'), 'error')
      return
    }
    if (!ListVolumeFiles) {
      // The Go binding isn't present — surface it unmistakably rather than
      // silently doing nothing (this is what "the button does nothing" looked
      // like: an early return with no feedback).
      const msg = '❌ ' + (t('volumeBindingMissing') || 'Image browsing is unavailable in this build (backend method not found).')
      showStatus(msg, 'error')
      try { window.alert(msg) } catch (e) { /* no-op */ }
      return
    }
    r.setSnapshotEntries([])
    r.setSelectedPaths(new Set())
    r.setExpandedDirs(new Set())
    r.setImageDisk(null)
    r.setImageTruncated(false)
    r.setBrowsingImage(true)
    showStatus(`💿 ${t('volumeReadingTree')}`, 'info')
    try {
      const res = await ListVolumeFiles(
        r.restorePBSID || '',
        r.selectedSnapshot.backup_id || r.restoreBackupId,
        r.selectedSnapshot.id,
        r.selectedSnapshot.backup_type || 'vm',
        disk,
        partIndex,
        forceRefresh
      )
      const entries = Array.isArray(res) ? res : ((res && res.entries) || [])
      r.setSnapshotEntries(entries)
      r.setImageDisk(disk)
      r.setImagePartIndex(partIndex)
      r.setImageCwd('/')
      r.setSelectedInfo(new Map())
      let truncated = false
      if (LastVolumeListTruncated) { try { truncated = await LastVolumeListTruncated() } catch (e) { /* ignore */ } }
      r.setImageTruncated(truncated)
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
      r.setBrowsingImage(false)
    }
  }

  // Navigate to a directory inside the image: fetch just that level from the
  // Go-side cache. The webview never holds more than one directory of rows,
  // so there is nothing to truncate.
  const handleImageNavigate = async (dir) => {
    if (!ListVolumeDirectory || !r.selectedSnapshot || !r.imageDisk) return
    try {
      const rows = await ListVolumeDirectory(
        r.restorePBSID || '',
        r.selectedSnapshot.backup_id || r.restoreBackupId,
        r.selectedSnapshot.id,
        r.selectedSnapshot.backup_type || 'vm',
        r.imageDisk,
        r.imagePartIndex,
        dir
      )
      r.setSnapshotEntries(rows || [])
      r.setImageCwd(dir)
    } catch (err) {
      showStatus('❌ ' + (err && err.message ? err.message : String(err)), 'error')
    }
  }

  const toggleImageSelect = (entry) => {
    const next = new Set(r.selectedPaths)
    const info = new Map(r.selectedInfo)
    if (next.has(entry.path)) {
      next.delete(entry.path)
      info.delete(entry.path)
    } else {
      next.add(entry.path)
      info.set(entry.path, { size: entry.size || 0 })
    }
    r.setSelectedPaths(next)
    r.setSelectedInfo(info)
  }

  // Back from a partition's folders to the disk's partition list.
  const handleBackToParts = () => {
    r.setSnapshotEntries([]); r.setSelectedPaths(new Set()); r.setSelectedInfo(new Map()); r.setImageDisk(null); r.setImageCwd('/')
  }

  return { handleListPartitions, handleBrowseImageDisk, handleImageNavigate, toggleImageSelect, handleBackToParts }
}
