import { useTranslation } from '../../i18n/i18nContext'
import { absOriginPath } from '../../lib/snapshots'
import { formatBytes } from '../../lib/format'

// One entry of a file backup's tree, and its children when expanded,
// rendered recursively from the children map. Depth is only used for the
// visual indent.
export default function TreeNode({ r, entry, childrenByDir, depth }) {
  const { t } = useTranslation()
  const isExpanded = r.expandedDirs.has(entry.path)
  const isSelected = r.selectedPaths.has(entry.path)
  const indent = { paddingLeft: `${depth * 16}px` }
  const origin = absOriginPath(r.snapshotMeta, entry.path)
  const rowTitle = origin ? t('originTooltip').replace('{path}', origin) : entry.path
  return (
    <div>
      <div
        title={rowTitle}
        onClick={(e) => r.handleRowClick(e, entry)}
        style={{ ...indent, display: 'flex', alignItems: 'center', padding: '4px 8px', cursor: 'pointer', borderBottom: '1px solid var(--nc-border-soft)', background: isSelected ? 'var(--nc-row-selected)' : undefined }}>
        <input
          type="checkbox"
          checked={isSelected}
          onClick={(e) => e.stopPropagation()}
          onChange={() => { r.togglePathSelection(entry.path); r.setLastClickedPath(entry.path) }}
          style={{ marginRight: '8px' }}
        />
        {/* Explicit expand/collapse control. The folder glyph alone was the
            only affordance before, and it was not obvious a row could open. */}
        {entry.is_dir ? (
          <button
            type="button"
            className="nc-caret"
            aria-label={isExpanded ? t('collapse') : t('expand')}
            aria-expanded={isExpanded}
            onClick={(e) => { e.stopPropagation(); r.toggleDir(entry.path) }}
          >
            {isExpanded ? '▾' : '▸'}
          </button>
        ) : (
          <span className="nc-caret-spacer" />
        )}
        <span style={{ marginRight: '6px' }}>
          {entry.is_dir ? (isExpanded ? '📂' : '📁') : '📄'}
        </span>
        <span
          onClick={() => entry.is_dir && r.toggleDir(entry.path)}
          style={{ flex: 1, cursor: entry.is_dir ? 'pointer' : 'default', fontSize: '14px' }}
        >
          {entry.path.split('/').pop() || entry.path}
        </span>
        <span className="nc-size-col">{formatBytes(r.entrySize(entry))}</span>
      </div>
      {entry.is_dir && isExpanded && (childrenByDir.get(entry.path) || []).map(child =>
        <TreeNode key={child.path} r={r} entry={child} childrenByDir={childrenByDir} depth={depth + 1} />
      )}
    </div>
  )
}
