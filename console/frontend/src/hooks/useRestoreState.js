import { useState } from 'react'

// Everything the restore screen holds: which snapshot, what of it is shown
// and selected, how and where it is restored, and the search across
// snapshots. Effects and handlers are in useRestore.
export default function useRestoreState() {
  const [snapshots, setSnapshots] = useState([])
  const [restoreBackupId, setRestoreBackupId] = useState('')
  const [showSnapshots, setShowSnapshots] = useState(false)
  const [restorePBSID, setRestorePBSID] = useState('')
  const [selectedSnapshot, setSelectedSnapshot] = useState(null) // { id, unix, time }
  const [snapshotMeta, setSnapshotMeta] = useState(null)         // .nimbus_backup_meta.json sidecar (null if legacy)
  const [snapshotEntries, setSnapshotEntries] = useState([])     // flat list from backend
  const [imageDisk, setImageDisk] = useState(null)                // volume mode: the .img.fidx being browsed
  const [imageTruncated, setImageTruncated] = useState(false)     // volume tree hit the entry cap
  const [browsingImage, setBrowsingImage] = useState(false)       // image walk in flight (button spinner)
  const [imagePartitions, setImagePartitions] = useState(null)    // partition picker rows (null = not loaded)
  const [imagePartIndex, setImagePartIndex] = useState(0)         // which partition is open (0 = none)
  const [loadingParts, setLoadingParts] = useState(false)
  const [imageCwd, setImageCwd] = useState('/')                  // current directory inside the image
  const [imageSort, setImageSort] = useState({ key: 'name', dir: 1 }) // dir: 1 asc, -1 desc
  const [selectedInfo, setSelectedInfo] = useState(new Map())    // path -> {size} for cross-directory selections
  const [packageAsZip, setPackageAsZip] = useState(false)        // unified workflow: zip to a file instead of restoring to a folder
  const [expandedDirs, setExpandedDirs] = useState(new Set())     // expanded paths in tree
  const [selectedPaths, setSelectedPaths] = useState(new Set())   // selected entry paths
  const [lastClickedPath, setLastClickedPath] = useState(null) // shift-range anchor
  const [restoreDestPath, setRestoreDestPath] = useState('')
  // 'original' (in-place), 'alternate_abs' (preserve tree), 'alternate_flat' (strip prefix)
  const [restoreMode, setRestoreMode] = useState('alternate_abs')
  const [restoreAllowCrossHost, setRestoreAllowCrossHost] = useState(false)
  // alternate sub-mode toggle: true = abs (keep tree), false = flat. Default flat per spec.
  const [restoreKeepTree, setRestoreKeepTree] = useState(false)
  const [restoreOptions, setRestoreOptions] = useState({
    overwrite: false,
    timestamps: true,
    acls: false, // disabled in UI until NTFS sidecar lands
    ads: false   // disabled in UI until NTFS sidecar lands
  })
  const [restoreLoading, setRestoreLoading] = useState(false)
  const [downloading, setDownloading] = useState(false)
  // Unified task progress for scan / restore / zip download. Fed by every
  // restore:progress event; visible from first event until 100% (with a short
  // linger) — it does NOT blink between emits, unlike the old status toasts.
  const [taskProg, setTaskProg] = useState(null) // {pct,msg,done,total,bps,eta,indet}

  // ===== file search across snapshots =====
  const [showSearch, setShowSearch] = useState(false)
  const [searchQuery, setSearchQuery] = useState('')
  const [searchMode, setSearchMode] = useState('name')      // 'name' | 'regex' | 'path'
  const [searchFrom, setSearchFrom] = useState('')           // yyyy-mm-dd
  const [searchTo, setSearchTo] = useState('')               // yyyy-mm-dd
  const [searchAssembleMissing, setSearchAssembleMissing] = useState(false)
  const [searchRunning, setSearchRunning] = useState(false)
  const [searchProgress, setSearchProgress] = useState({ percent: 0, message: '' })
  const [searchResult, setSearchResult] = useState(null)     // { hits, snapshots_*, truncated, cancelled }

  return {
    snapshots, setSnapshots, restoreBackupId, setRestoreBackupId, showSnapshots, setShowSnapshots,
    restorePBSID, setRestorePBSID, selectedSnapshot, setSelectedSnapshot, snapshotMeta, setSnapshotMeta,
    snapshotEntries, setSnapshotEntries, imageDisk, setImageDisk, imageTruncated, setImageTruncated,
    browsingImage, setBrowsingImage, imagePartitions, setImagePartitions, imagePartIndex, setImagePartIndex,
    loadingParts, setLoadingParts, imageCwd, setImageCwd, imageSort, setImageSort,
    selectedInfo, setSelectedInfo, packageAsZip, setPackageAsZip, expandedDirs, setExpandedDirs,
    selectedPaths, setSelectedPaths, lastClickedPath, setLastClickedPath, restoreDestPath, setRestoreDestPath,
    restoreMode, setRestoreMode, restoreAllowCrossHost, setRestoreAllowCrossHost,
    restoreKeepTree, setRestoreKeepTree, restoreOptions, setRestoreOptions,
    restoreLoading, setRestoreLoading, downloading, setDownloading, taskProg, setTaskProg,
    showSearch, setShowSearch, searchQuery, setSearchQuery, searchMode, setSearchMode,
    searchFrom, setSearchFrom, searchTo, setSearchTo, searchAssembleMissing, setSearchAssembleMissing,
    searchRunning, setSearchRunning, searchProgress, setSearchProgress, searchResult, setSearchResult,
  }
}
