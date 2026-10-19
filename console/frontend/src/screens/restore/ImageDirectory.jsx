import { useTranslation } from '../../i18n/i18nContext'
import { formatBytes } from '../../lib/format'

// One folder of an open partition: where it is, and everything at this level.
export default function ImageDirectory({ r }) {
  const { t } = useTranslation()
  const sortMark = (key) => r.imageSort.key === key ? (r.imageSort.dir > 0 ? ' ▲' : ' ▼') : ''
  return (
    <>
      <div style={{padding: '6px 10px', borderBottom: '1px solid var(--nc-border-soft)', fontSize: '12px', display: 'flex', gap: '8px', alignItems: 'center', flexWrap: 'wrap'}}>
        <span style={{color: 'var(--nc-accent)', fontWeight: 600}}>💿 {String(r.imageDisk).replace('.img.fidx','')} · {t('partition')} {r.imagePartIndex}</span>
        <button className="btn" style={{padding: '1px 8px', fontSize: '11px'}}
          onClick={r.handleBackToParts}>
          ↩ {t('volumeBackToParts')}
        </button>
      </div>
      {/* breadcrumb: every segment navigates */}
      <div className="nc-crumbs">
        <button className="nc-crumb" onClick={() => r.handleImageNavigate('/')}>💿 /</button>
        {r.imageCwd.split('/').filter(Boolean).map((seg, i, arr) => {
          const target = '/' + arr.slice(0, i + 1).join('/')
          return (
            <span key={target}>
              <span className="nc-crumb-sep">›</span>
              <button className="nc-crumb" onClick={() => r.handleImageNavigate(target)}>{seg}</button>
            </span>
          )
        })}
      </div>
      {/* directory table: everything at this level, scrollable, sortable */}
      <div className="nc-dir-scroll">
        <table className="nc-dir-table">
          <thead>
            <tr>
              <th style={{width: '30px'}}></th>
              <th className="sortable" onClick={() => r.setImageSort(sv => ({key: 'name', dir: sv.key === 'name' ? -sv.dir : 1}))}>
                {t('partName')}{sortMark('name')}
              </th>
              <th className="sortable col-date" onClick={() => r.setImageSort(sv => ({key: 'mtime', dir: sv.key === 'mtime' ? -sv.dir : -1}))}>
                {t('colModified')}{sortMark('mtime')}
              </th>
              <th className="sortable col-size" onClick={() => r.setImageSort(sv => ({key: 'size', dir: sv.key === 'size' ? -sv.dir : -1}))}>
                {t('colSize')}{sortMark('size')}
              </th>
            </tr>
          </thead>
          <tbody>
            {[...r.snapshotEntries].sort((a, b) => {
              if (a.is_dir !== b.is_dir) return a.is_dir ? -1 : 1
              let c = 0
              if (r.imageSort.key === 'size') c = (a.size || 0) - (b.size || 0)
              else if (r.imageSort.key === 'mtime') c = (a.mtime || 0) - (b.mtime || 0)
              else c = a.path.localeCompare(b.path, undefined, { sensitivity: 'base' })
              return c * r.imageSort.dir
            }).map(e => (
              <tr key={e.path} className={r.selectedPaths.has(e.path) ? 'selected' : ''}>
                <td>
                  <input type="checkbox" checked={r.selectedPaths.has(e.path)} onChange={() => r.toggleImageSelect(e)} />
                </td>
                <td>
                  {e.is_dir ? (
                    <button className="nc-dirlink" onClick={() => r.handleImageNavigate(e.path)}>
                      📁 {e.path.split('/').pop()}
                    </button>
                  ) : (
                    <span>📄 {e.path.split('/').pop()}</span>
                  )}
                </td>
                <td className="col-date">{e.mtime ? new Date(e.mtime * 1000).toLocaleString() : '—'}</td>
                <td className="col-size">{formatBytes(e.size || 0)}</td>
              </tr>
            ))}
          </tbody>
        </table>
        {r.snapshotEntries.length === 0 && <div className="hint-text" style={{padding: '10px'}}>{t('pickerNoSubfolders')}</div>}
      </div>
    </>
  )
}
