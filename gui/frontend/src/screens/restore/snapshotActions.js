import { ListSnapshots, ListSnapshotContents, GetSnapshotMeta } from '../../lib/bindings'
import { isVolumeSnapshot } from '../../lib/snapshots'

// Find snapshots, open one, reload one. r is the restore model (useRestore),
// made per render over the current state.
export default function makeSnapshotActions(r, { showStatus, t }) {
  const handleListSnapshots = async () => {
    if (!ListSnapshots) {
      showStatus('❌ ' + t('errNoRuntime'), 'error')
      return
    }
    if (!r.restoreBackupId) {
      showStatus('❌ ' + t('errBackupIdRequired'), 'error')
      return
    }

    showStatus('🔍 ' + t('searchingSnapshots'), 'info')
    r.setSelectedSnapshot(null)
    r.setSnapshotEntries([])
    r.setSelectedPaths(new Set())
    r.setExpandedDirs(new Set())

    try {
      const snaps = await ListSnapshots(r.restorePBSID || '', r.restoreBackupId)
      r.setSnapshots(snaps || [])
      r.setShowSnapshots(true)
      showStatus(`✅ ${t('snapshotsFound').replace('{n}', snaps.length)}`, 'success')
    } catch (err) {
      showStatus(`❌ ${err}`, 'error')
    }
  }

  const handleSelectSnapshot = async (snap, forceRefresh = false) => {
    if (!ListSnapshotContents) {
      showStatus('❌ ' + t('errNoRuntime'), 'error')
      return
    }
    r.setSelectedSnapshot(snap)
    r.setSnapshotMeta(null)
    r.setSnapshotEntries([])
    r.setSelectedPaths(new Set())
    r.setExpandedDirs(new Set())
    r.setImageDisk(null)
    r.setImageTruncated(false)
    // Volume (disk image) snapshots have no pxar catalog to list — the panel
    // shows the disks with a Browse-files button per disk instead.
    if (isVolumeSnapshot(snap)) {
      showStatus(`💿 ${t('volumeSnapSelected')}`, 'info')
      return
    }
    showStatus(`📥 ${t('loadingSnapshotContents')}`, 'info')
    const effectiveBackupId = snap.backup_id || r.restoreBackupId
    try {
      // Backend uses the snapshot's actual backup_id (snap.backup_id) so multi-folder
      // backups list their real contents, not the partial search term.
      const entries = await ListSnapshotContents(r.restorePBSID || '', effectiveBackupId, snap.unix, forceRefresh)
      r.setSnapshotEntries(entries || [])
      showStatus(`✅ ${(entries || []).length} ${t('entriesLoaded')}`, 'success')
    } catch (err) {
      showStatus(`❌ ${err}`, 'error')
    }
    // Meta is informational — fire-and-forget. The listing call above has
    // already populated the cache, so this is a cheap cache hit. Failure is
    // silent: legacy snapshots simply have no sidecar.
    if (GetSnapshotMeta) {
      try {
        const meta = await GetSnapshotMeta(r.restorePBSID || '', effectiveBackupId, snap.unix)
        if (meta) r.setSnapshotMeta(meta)
      } catch (_err) {
        // ignored — banner stays hidden
      }
    }
  }

  const handleReloadSnapshot = async () => {
    if (!r.selectedSnapshot) return
    await handleSelectSnapshot(r.selectedSnapshot, true)
  }

  return { handleListSnapshots, handleSelectSnapshot, handleReloadSnapshot }
}
