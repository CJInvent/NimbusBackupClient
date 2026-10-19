import { visibleOrder } from '../../lib/snapshots'

// Expanding folders and selecting entries in a file backup's tree.
export default function makeSelectionActions(r) {
  const toggleDir = (path) => {
    r.setExpandedDirs(prev => {
      const next = new Set(prev)
      if (next.has(path)) next.delete(path)
      else next.add(path)
      return next
    })
  }

  const togglePathSelection = (path) => {
    r.setSelectedPaths(prev => {
      const next = new Set(prev)
      if (next.has(path)) next.delete(path)
      else next.add(path)
      return next
    })
  }

  // Row click semantics: ctrl(⌘)+click toggles the row; shift+click selects
  // the contiguous visible range from the last clicked row; plain click on a
  // dir expands/collapses (files: toggles the checkbox).
  const handleRowClick = (e, entry) => {
    if (e.shiftKey && r.lastClickedPath) {
      e.preventDefault()
      const order = visibleOrder(r.snapshotEntries, r.expandedDirs)
      const a = order.indexOf(r.lastClickedPath)
      const b = order.indexOf(entry.path)
      if (a !== -1 && b !== -1) {
        const [lo, hi] = a < b ? [a, b] : [b, a]
        r.setSelectedPaths(prev => {
          const next = new Set(prev)
          for (let i = lo; i <= hi; i++) next.add(order[i])
          return next
        })
        return
      }
    }
    if (e.ctrlKey || e.metaKey) {
      togglePathSelection(entry.path)
      r.setLastClickedPath(entry.path)
      return
    }
    r.setLastClickedPath(entry.path)
    if (entry.is_dir) toggleDir(entry.path)
    else togglePathSelection(entry.path)
  }

  return { toggleDir, togglePathSelection, handleRowClick }
}
