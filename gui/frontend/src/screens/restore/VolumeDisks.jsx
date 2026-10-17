import { useTranslation } from '../../i18n/i18nContext'
import { snapshotDisks } from '../../lib/snapshots'
import PartitionTable from './PartitionTable'

// A disk-image snapshot: pick a disk, then a partition.
export default function VolumeDisks({ r }) {
  const { t } = useTranslation()
  return (
    <div style={{padding: '12px'}}>
      <p style={{margin: 0, fontWeight: 600, color: 'var(--nc-accent)'}}>💿 {t('volumeSnapTitle')}</p>
      <p style={{margin: '6px 0 10px', fontSize: '13px', color: 'var(--nc-text-dim)'}}>{t('volumeSnapExplainV2')}</p>

      {/* Step 1 — pick a disk */}
      {snapshotDisks(r.selectedSnapshot).map((d, i) => (
        <div key={i} style={{display: 'flex', alignItems: 'center', gap: '10px', padding: '3px 0'}}>
          <span className="mono" style={{fontSize: '12px'}}>💿 {String(d).replace('.img.fidx','')}</span>
          <button className="btn" style={{padding: '2px 10px', fontSize: '12px'}} disabled={r.loadingParts} onClick={() => r.handleListPartitions(d)}>
            {r.loadingParts ? '⏳ ' : '🔍 '}{t('volumeShowParts')}
          </button>
        </div>
      ))}

      {/* Step 2 — pick a partition. Every partition is listed,
          browsable or not, with its filesystem and sizes, so the
          user chooses instead of us guessing (we used to open the
          first NTFS volume, which was the WinRE partition). */}
      {r.imagePartitions && <PartitionTable r={r} />}

      <p style={{margin: '10px 0 0', fontSize: '12px', color: 'var(--nc-text-dim)'}}>{t('volumeSnapRestoreHint')}</p>
    </div>
  )
}
