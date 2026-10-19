import { useTranslation } from '../../i18n/i18nContext'
import { isVolumeSnapshot } from '../../lib/snapshots'
import { formatBytes } from '../../lib/format'
import VolumeDisks from './VolumeDisks'
import ImageDirectory from './ImageDirectory'
import FileTree from './FileTree'

// Snapshot navigation tree: a disk image's disks and partitions, one
// partition's folders, or a file backup's tree; and what is selected.
export default function SnapshotContents({ r }) {
  const { t } = useTranslation()
  return (
    <div style={{marginTop: '24px'}}>
      <div style={{display: 'flex', alignItems: 'center', gap: '12px', flexWrap: 'wrap'}}>
        <h3 style={{margin: 0}}>📂 {t('snapshotContents')} — {r.selectedSnapshot.time}</h3>
        <button
          className="btn"
          type="button"
          onClick={r.handleReloadSnapshot}
          title={t('reloadTreeHint') || 'Bypass local cache and re-download the snapshot tree'}
          style={{padding: '4px 10px', fontSize: '13px'}}
        >
          🔄 {t('reloadTree') || 'Recharger'}
        </button>
      </div>
      <p style={{fontSize: '13px', color: 'var(--nc-text-dim)', marginBottom: '8px', marginTop: '6px'}}>
        {t('treeHint')}
      </p>
      <div style={{
        border: '1px solid var(--nc-border)',
        borderRadius: '8px',
        // Image-directory mode has its own inner scroller
        // (.nc-dir-scroll); a second one here produced the double
        // scrollbar. Only the tree/partition views need this wrapper
        // to scroll.
        ...(r.imageDisk ? {} : { maxHeight: '360px', overflowY: 'auto' }),
        backgroundColor: 'var(--nc-panel)'
      }}>
        {r.snapshotEntries.length === 0 && r.selectedSnapshot && isVolumeSnapshot(r.selectedSnapshot) ? (
          <VolumeDisks r={r} />
        ) : r.snapshotEntries.length === 0 ? (
          <p style={{padding: '12px', color: 'var(--nc-text-dim)'}}>{t('loadingOrEmpty')}</p>
        ) : r.imageDisk ? (
          <ImageDirectory r={r} />
        ) : (
          <FileTree r={r} />
        )}
      </div>
      <p style={{marginTop: '6px', fontSize: '12px', color: 'var(--nc-text-dim)'}}>
        {r.selectedPaths.size === 0
          ? t('selectionEmptyAllSize').replace('{size}', formatBytes(r.selectionBytes))
          : t('selectionCountSize')
              .replace('{n}', r.selectedPaths.size)
              .replace('{size}', formatBytes(r.selectionBytes))}
      </p>

    </div>
  )
}
