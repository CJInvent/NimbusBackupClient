import { useEffect, useMemo } from 'react'
import { EventsOn } from '../lib/bindings'
import useRestoreState from './useRestoreState'
import makeSnapshotActions from '../screens/restore/snapshotActions'
import makeSearchActions from '../screens/restore/searchActions'
import makeSelectionActions from '../screens/restore/selectionActions'
import makeImageActions from '../screens/restore/imageActions'
import makeRestoreActions from '../screens/restore/restoreActions'

// The restore model: state (useRestoreState), what is derived from it, the
// events it listens to, and its handlers. The restore screen renders it.
export default function useRestore({ t, showStatus, config, hostname, systemInfo, defaultPBSID, openPicker }) {
  const s = useRestoreState()

  // Default the backup ID to look up to this machine's.
  useEffect(() => {
    if (!s.restoreBackupId && (config['backup-id'] || hostname)) {
      s.setRestoreBackupId(config['backup-id'] || hostname)
    }
  }, [config['backup-id'], hostname])

  // When the selected snapshot has no sidecar (legacy) or its OS doesn't match
  // the current host, in-place is impossible. Snap the mode back to alternate
  // so a stale "original" selection doesn't survive across snapshot switches.
  useEffect(() => {
    if (s.restoreMode !== 'original') return
    if (!s.snapshotMeta) { s.setRestoreMode('alternate_abs'); return }
    if (!s.snapshotMeta.original_path) { s.setRestoreMode('alternate_abs'); return }
    if (systemInfo.os && s.snapshotMeta.os && systemInfo.os !== s.snapshotMeta.os) {
      s.setRestoreMode('alternate_abs')
    }
  }, [s.snapshotMeta, systemInfo.os, s.restoreMode])

  // Sync restore PBS dropdown with default once it's loaded
  useEffect(() => {
    if (!s.restorePBSID && defaultPBSID) {
      s.setRestorePBSID(defaultPBSID)
    }
  }, [defaultPBSID])

  // Listen to restore events
  useEffect(() => {
    if (!EventsOn) return
    const unsubP = EventsOn('restore:progress', (data) => {
      const pct = Math.round(data.percent || 0)
      // Indeterminate when the phase can't know its size (no totals yet).
      const indet = !(data.total_bytes > 0) && pct === 0
      s.setTaskProg({
        pct,
        msg: data.message || '',
        done: data.done_bytes || 0,
        total: data.total_bytes || 0,
        bps: data.bps || 0,
        eta: (typeof data.eta_seconds === 'number' && data.eta_seconds >= 0) ? data.eta_seconds : null,
        indet,
      })
      if (pct >= 100) {
        // Linger briefly at 100 so completion is visible, then clear.
        setTimeout(() => s.setTaskProg(p => (p && p.pct >= 100 ? null : p)), 900)
      }
    })
    const unsubC = EventsOn('restore:complete', (data) => {
      s.setRestoreLoading(false)
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
      s.setSearchProgress({ percent: Math.round((data.percent || 0) * 100), message: data.message || '' })
    })
    return () => { if (unsub) unsub() }
  }, [])

  // Total bytes the current selection will restore. Selecting a directory pulls
  // in all its descendants, so we sum every file that is itself selected or
  // lives under a selected path. An empty selection means "restore everything",
  // so we sum the whole snapshot. Memoized — snapshots can hold 100k+ entries.
  const selectionBytes = useMemo(() => {
    if (s.imageDisk) {
      // Image mode: the webview only holds the current directory, so sizes of
      // selections (files AND folders — folders carry rolled-up sizes) are
      // tracked at click time in selectedInfo.
      let sum = 0
      for (const v of s.selectedInfo.values()) sum += (v.size || 0)
      return sum
    }
    if (!s.snapshotEntries.length) return 0
    if (s.selectedPaths.size === 0) {
      return s.snapshotEntries.reduce((sum, e) => e.is_dir ? sum : sum + (e.size || 0), 0)
    }
    const sel = Array.from(s.selectedPaths)
    let sum = 0
    for (const e of s.snapshotEntries) {
      if (e.is_dir) continue
      for (const p of sel) {
        if (e.path === p || e.path.startsWith(p + '/')) { sum += (e.size || 0); break }
      }
    }
    return sum
  }, [s.snapshotEntries, s.selectedPaths, s.imageDisk, s.selectedInfo])

  // The option matrix for the unified restore workflow. ONE place decides
  // which options apply to the current source + packaging, and WHY not —
  // the reason is shown as the tooltip on every greyed option.
  //  - Image backups from NTFS: permissions + ADS come straight from the
  //    parsed volume (no sidecar needed — the image contains them).
  //  - Image backups from FAT/exFAT: the format stores neither -> greyed.
  //  - Directory (pxar) backups: capture of perms/ADS isn't implemented yet
  //    on the backup side -> greyed, honestly labelled.
  //  - ZIP packaging can't represent either -> greyed while ZIP is ticked.
  const restoreMatrix = useMemo(() => {
    const imgFs = s.imageDisk && s.imagePartitions
      ? (s.imagePartitions.rows.find(r => r.index === s.imagePartIndex)?.filesystem || '')
      : ''
    const m = {
      zip:   { on: true, why: '' },
      mtime: { on: true, why: '' },
      acl:   { on: false, why: '' },
      ads:   { on: false, why: '' },
    }
    if (s.imageDisk) {
      if (imgFs === 'ntfs') {
        m.acl = { on: true, why: '' }
        m.ads = { on: true, why: '' }
      } else {
        m.acl = { on: false, why: t('naFat') }
        m.ads = { on: false, why: t('naFat') }
      }
    } else {
      m.acl = { on: false, why: t('naSidecar') }
      m.ads = { on: false, why: t('naSidecar') }
    }
    if (s.packageAsZip) {
      m.acl = { on: false, why: t('naZip') }
      m.ads = { on: false, why: t('naZip') }
      m.mtime = { on: false, why: t('naZip') }
    }
    return m
  }, [s.imageDisk, s.imagePartitions, s.imagePartIndex, s.packageAsZip])

  // Folder size = sum of every file beneath it. Computed once per entry list
  // (a Map keyed by dir path) so the tree can show a size column for folders
  // as well as files, for both backup types.
  const dirSizes = useMemo(() => {
    const m = new Map()
    for (const e of s.snapshotEntries) {
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
  }, [s.snapshotEntries])

  const entrySize = (entry) => entry.is_dir ? (dirSizes.get(entry.path) || 0) : (entry.size || 0)

  const r = { ...s, selectionBytes, restoreMatrix, entrySize }
  const ctx = { showStatus, t, hostname, openPicker }
  const snaps = makeSnapshotActions(r, ctx)
  return {
    ...r,
    ...snaps,
    ...makeSearchActions(r, ctx, snaps.handleSelectSnapshot),
    ...makeSelectionActions(r),
    ...makeImageActions(r, ctx),
    ...makeRestoreActions(r, ctx),
  }
}
