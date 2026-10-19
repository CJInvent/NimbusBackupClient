import {
  RestoreSnapshot, RestoreFilesFromVolume, CheckDownloadSpace, DownloadSelection,
  DownloadFilesFromVolume, CancelVolumeFileRestore,
} from '../../lib/bindings'
import { formatBytes } from '../../lib/format'

// Restore the selection to a folder (or in place), download it as a ZIP, or
// cancel. r carries the derived selectionBytes and restoreMatrix.
export default function makeRestoreActions(r, { showStatus, t, openPicker }) {
  const handleBrowseRestoreDest = async () => {
    const dir = await openPicker({
      mode: 'folder',
      initialPath: r.restoreDestPath || '',
      needBytes: r.selectionBytes || 0,
    })
    if (dir) r.setRestoreDestPath(dir)
  }

  const handleRestoreSnapshot = async () => {
    if (!RestoreSnapshot) {
      showStatus('❌ ' + t('errNoRuntime'), 'error')
      return
    }
    if (!r.selectedSnapshot) {
      showStatus('❌ ' + t('selectSnapshotFirst'), 'error')
      return
    }

    // Resolve effective mode. The UI radio is binary (in-place / alternate);
    // the alternate sub-mode comes from the "keep tree" toggle.
    let effectiveMode = r.restoreMode
    if (r.restoreMode !== 'original') {
      effectiveMode = r.restoreKeepTree ? 'alternate_abs' : 'alternate_flat'
    }

    if (effectiveMode !== 'original' && !r.restoreDestPath) {
      showStatus('❌ ' + t('destinationRequired'), 'error')
      return
    }

    // In-place: scary, get explicit confirmation. confirm() is a stopgap until
    // we wire a real modal — for the alpha phase it's enough and the message
    // is precise about what will happen.
    if (effectiveMode === 'original') {
      const target = r.snapshotMeta?.original_path || '?'
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
    const includes = Array.from(r.selectedPaths)
    if (r.imageDisk && includes.length === 0) {
      showStatus('❌ ' + t('imageRestoreNeedsSelection'), 'error')
      return
    }

    r.setRestoreLoading(true)
    showStatus(`🔄 ${t('statusRestoring').replace('{time}', r.selectedSnapshot.time)}`, 'info')

    try {
      if (r.imageDisk) {
        // Volume backup: restore the selected files OUT of the disk image.
        // The pxar path cannot serve this — a vm snapshot has no
        // backup.pxar.didx, which is why the old code 400'd at PBS.
        await RestoreFilesFromVolume(
          r.restorePBSID || '',
          r.selectedSnapshot.backup_id || r.restoreBackupId,
          r.selectedSnapshot.id,
          r.selectedSnapshot.backup_type || 'vm',
          r.imageDisk,
          r.imagePartIndex,
          includes,
          r.restoreDestPath,
          r.restoreKeepTree,
          r.restoreOptions.overwrite,
          r.restoreMatrix.mtime.on && r.restoreOptions.timestamps,
          r.restoreMatrix.acl.on && r.restoreOptions.acls,
          r.restoreMatrix.ads.on && r.restoreOptions.ads,
          r.selectionBytes || 0
        )
        r.setRestoreLoading(false)
        showStatus('✅ ' + t('restoreDone'), 'success')
        return
      }
      await RestoreSnapshot(
        r.restorePBSID || '',
        r.selectedSnapshot.backup_id || r.restoreBackupId,
        r.selectedSnapshot.id,
        r.restoreDestPath,
        effectiveMode,
        includes,
        r.restoreAllowCrossHost,
        r.restoreOptions.acls,
        r.restoreOptions.ads,
        r.restoreOptions.timestamps,
        r.restoreOptions.overwrite
      )
      // Completion arrives via the restore:complete event.
    } catch (err) {
      r.setRestoreLoading(false)
      showStatus(`❌ ${err}`, 'error')
    }
  }

  // Download the current selection as a ZIP. Pre-flight space check: BLOCK
  // if it cannot fit on the destination drive, WARN (confirm dialog) if it
  // would push the drive to >= 90% used. The Go side re-enforces both rules
  // authoritatively.
  const handleDownloadSelection = async () => {
    if (!DownloadSelection || r.selectedPaths.size === 0) return
    const paths = Array.from(r.selectedPaths)
    // The user explicitly ticked "Package as ZIP" — a single file becomes a
    // one-entry zip, exactly as asked. (The old single->direct-file shortcut
    // silently overrode the request.)
    const defaultName = `${r.selectedSnapshot.backup_id}-${(r.selectedSnapshot.time || '').replace(/[: ]/g, '-')}.zip`
    const dest = await openPicker({
      mode: 'save',
      initialPath: r.restoreDestPath || '',
      defaultFileName: defaultName,
      needBytes: r.selectionBytes || 0,
    })
    if (!dest) return // user cancelled

    // Pre-flight space math (advisory UX; Go re-checks and enforces).
    try {
      const sc = await CheckDownloadSpace(dest, r.selectionBytes || 0)
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

    r.setDownloading(true)
    try {
      if (r.imageDisk) {
        await DownloadFilesFromVolume(
          r.restorePBSID || '',
          r.selectedSnapshot.backup_id || r.restoreBackupId,
          r.selectedSnapshot.id,
          r.selectedSnapshot.backup_type || 'vm',
          r.imageDisk,
          r.imagePartIndex,
          paths,
          dest,
          true, // always a zip — that is what the ticked option means
          r.selectionBytes || 0
        )
      } else {
        await DownloadSelection(
          r.restorePBSID || '',
          r.selectedSnapshot.backup_id || r.restoreBackupId,
          r.selectedSnapshot.id,
          paths,
          dest,
          true, // always a zip — that is what the ticked option means
          r.selectionBytes || 0
        )
      }
      showStatus('✅ ' + t('downloadDone').replace('{dest}', dest), 'success')
    } catch (e) {
      showStatus('❌ ' + (e && e.message ? e.message : String(e)), 'error')
    } finally {
      r.setDownloading(false)
    }
  }

  // Cancel an in-flight restore. Image restores are cancellable mid-run on the
  // Go side (CancelVolumeFileRestore); the button appears only while one is active.
  const handleCancelRestore = async () => {
    try {
      if (CancelVolumeFileRestore) await CancelVolumeFileRestore()
    } catch (e) { /* best-effort */ }
    showStatus('✖ ' + t('restoreCancelled'), 'info')
  }

  return { handleBrowseRestoreDest, handleRestoreSnapshot, handleDownloadSelection, handleCancelRestore }
}
