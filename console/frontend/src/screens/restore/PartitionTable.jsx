import { useTranslation } from '../../i18n/i18nContext'
import { formatBytes } from '../../lib/format'

// Every partition of the chosen disk, with Browse files on the readable ones.
export default function PartitionTable({ r }) {
  const { t } = useTranslation()
  return (
    <div style={{marginTop: '12px'}}>
      <p style={{margin: '0 0 6px', fontSize: '12px', fontWeight: 600}}>
        {t('volumePartsOn')} <span className="mono">{String(r.imagePartitions.disk).replace('.img.fidx','')}</span>
      </p>
      <table className="nc-part-table">
        <thead>
          <tr>
            <th>#</th>
            <th>{t('partName')}</th>
            <th>{t('partType')}</th>
            <th>{t('partFs')}</th>
            <th style={{textAlign: 'right'}}>{t('partUsed')}</th>
            <th style={{textAlign: 'right'}}>{t('partAllocated')}</th>
            <th></th>
          </tr>
        </thead>
        <tbody>
          {r.imagePartitions.rows.map(p => (
            <tr key={p.index} className={p.index === r.imagePartIndex ? 'selected' : ''}>
              <td>{p.index}</td>
              <td>{p.volume_label || p.name || '—'}</td>
              <td>{p.type}</td>
              <td className="mono">{p.filesystem}</td>
              <td style={{textAlign: 'right'}}>{p.used_known ? formatBytes(p.used_bytes) : '—'}</td>
              <td style={{textAlign: 'right'}}>{formatBytes(p.allocated_bytes)}</td>
              <td style={{textAlign: 'right'}}>
                {p.browsable ? (
                  <span style={{display: 'inline-flex', alignItems: 'center', gap: '8px'}}>
                    {p.file_table_bytes > 0 && (
                      <span className="hint-text" title={t('fileTableHint')}>
                        {t('fileTableLabel')} {formatBytes(p.file_table_bytes)}{p.file_table_frags > 1 ? ` · ${p.file_table_frags} ${t('fileTableFrags')}` : ''}
                      </span>
                    )}
                    <button className="btn btn-primary" style={{padding: '2px 10px', fontSize: '12px'}}
                      disabled={r.browsingImage}
                      onClick={() => r.handleBrowseImageDisk(r.imagePartitions.disk, p.index)}>
                      {r.browsingImage ? '⏳ ' : '📂 '}{t('volumeBrowseFiles')}
                    </button>
                  </span>
                ) : (
                  <span className="hint-text" title={p.reason}>{p.reason}</span>
                )}
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  )
}
