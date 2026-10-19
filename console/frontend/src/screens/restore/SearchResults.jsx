import { useTranslation } from '../../i18n/i18nContext'
import { formatBytes } from '../../lib/format'

// What a search found, each hit with "Restore this".
export default function SearchResults({ r }) {
  const { t } = useTranslation()
  const searchResult = r.searchResult
  return (
    <div style={{marginTop: '14px'}}>
      <p style={{fontSize: '13px', color: 'var(--nc-text)', margin: '0 0 6px 0'}}>
        {t('searchSummary')
          .replace('{hits}', searchResult.hits ? searchResult.hits.length : 0)
          .replace('{searched}', searchResult.snapshots_searched || 0)
          .replace('{assembled}', searchResult.snapshots_assembled || 0)}
        {searchResult.truncated ? ` ⚠️ ${t('searchTruncated').replace('{max}', 5000)}` : ''}
        {searchResult.cancelled ? ` ⚠️ ${t('searchCancelled')}` : ''}
      </p>
      {searchResult.snapshots_in_range === 0 && (
        <p style={{fontSize: '12px', color: 'var(--nc-warn)', margin: '0 0 8px 0'}}>
          💡 {t('searchNoSnapshotsInRange')}
        </p>
      )}
      {(searchResult.snapshots_skipped > 0 && !r.searchAssembleMissing) && (
        <p style={{fontSize: '12px', color: 'var(--nc-warn)', margin: '0 0 8px 0'}}>
          ⚠️ {t('searchSkippedWarning').replace('{n}', searchResult.snapshots_skipped)}
        </p>
      )}

      <div style={{
        border: '1px solid var(--nc-border)',
        borderRadius: '8px',
        maxHeight: '320px',
        overflowY: 'auto',
        backgroundColor: 'var(--nc-panel)'
      }}>
        {(!searchResult.hits || searchResult.hits.length === 0) ? (
          <p style={{padding: '12px', color: 'var(--nc-text-dim)'}}>{t('searchNoResults')}</p>
        ) : (
          searchResult.hits.map((hit, idx) => (
            <div
              key={`${hit.backup_id}-${hit.snapshot_time}-${hit.path}-${idx}`}
              title={hit.origin_path || hit.path}
              style={{
                display: 'flex', alignItems: 'center', gap: '10px',
                padding: '6px 10px', borderBottom: '1px solid var(--nc-border-soft)', fontSize: '13px'
              }}
            >
              <span style={{fontSize: '15px'}}>{hit.is_dir ? '📁' : '📄'}</span>
              <div style={{flex: 1, minWidth: 0}}>
                <div style={{fontWeight: 600, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap'}}>
                  {hit.path.split('/').pop() || hit.path}
                </div>
                <div style={{color: 'var(--nc-text-dim)', fontSize: '12px', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap'}}>
                  {hit.origin_path || hit.path}
                </div>
                <div style={{color: 'var(--nc-text-dim)', fontSize: '11px'}}>
                  {hit.backup_id} · {new Date(hit.snapshot_time * 1000).toLocaleString()}
                  {hit.is_dir ? '' : ` · ${formatBytes(hit.size)}`}
                </div>
              </div>
              <button
                className="btn"
                type="button"
                onClick={() => r.handleRestoreHit(hit)}
                style={{padding: '4px 10px', fontSize: '12px'}}
              >
                {t('searchRestoreThis')}
              </button>
            </div>
          ))
        )}
      </div>
    </div>
  )
}
