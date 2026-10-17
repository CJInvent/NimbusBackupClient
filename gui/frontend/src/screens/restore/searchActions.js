import { SearchFiles, CancelSearch } from '../../lib/bindings'
import { parseDateToUnix } from '../../lib/format'

// File search across snapshots, and opening a hit. selectSnapshot is the
// snapshot actions' handleSelectSnapshot.
export default function makeSearchActions(r, { showStatus, t, hostname }, selectSnapshot) {
  const handleSearch = async () => {
    if (!SearchFiles) {
      showStatus('❌ ' + t('errNoRuntime'), 'error')
      return
    }
    if (!r.searchQuery.trim()) {
      showStatus('❌ ' + t('searchQueryRequired'), 'error')
      return
    }
    const prefix = (r.restoreBackupId || hostname || '').trim()
    r.setSearchRunning(true)
    r.setSearchResult(null)
    r.setSearchProgress({ percent: 0, message: '' })
    try {
      const res = await SearchFiles(
        r.restorePBSID || '',
        prefix,
        r.searchQuery,
        r.searchMode,
        parseDateToUnix(r.searchFrom, false),
        parseDateToUnix(r.searchTo, true),
        r.searchAssembleMissing
      )
      r.setSearchResult(res)
      const n = (res && res.hits) ? res.hits.length : 0
      showStatus(`✅ ${t('searchDone').replace('{n}', n)}`, 'success')
    } catch (err) {
      showStatus(`❌ ${err}`, 'error')
    } finally {
      r.setSearchRunning(false)
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
    r.setRestoreBackupId(hit.backup_id)
    const iso = new Date(hit.snapshot_time * 1000).toISOString()
    const snap = {
      id: iso.slice(0, 19) + 'Z',
      backup_id: hit.backup_id,
      unix: hit.snapshot_time,
      time: new Date(hit.snapshot_time * 1000).toLocaleString(),
    }
    r.setShowSnapshots(true)
    await selectSnapshot(snap)
    r.setSelectedPaths(new Set([hit.path]))
    const parts = hit.path.split('/')
    const dirs = new Set()
    let acc = ''
    for (let i = 0; i < parts.length - 1; i++) {
      acc = acc ? `${acc}/${parts[i]}` : parts[i]
      dirs.add(acc)
    }
    r.setExpandedDirs(dirs)
  }

  return { handleSearch, handleCancelSearch, handleRestoreHit }
}
