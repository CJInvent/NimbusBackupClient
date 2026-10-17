import { useTranslation } from '../../i18n/i18nContext'

// Backup origin banner — driven by the .nimbus_backup_meta.json sidecar
export default function SnapshotMetaBanner({ snapshotMeta }) {
  const { t } = useTranslation()
  return (
    <div style={{
      marginTop: '20px',
      padding: '10px 14px',
      border: '1px solid color-mix(in srgb, var(--nc-accent) 35%, var(--nc-border))',
      backgroundColor: 'color-mix(in srgb, var(--nc-accent) 8%, var(--nc-panel-head))',
      borderRadius: '8px',
      fontSize: '13px',
      color: 'var(--nc-text)',
      display: 'grid',
      gridTemplateColumns: 'auto 1fr',
      columnGap: '12px',
      rowGap: '4px'
    }}>
      <strong>{t('metaOriginalPath') || 'Source d\'origine'}</strong>
      <span style={{fontFamily: 'monospace'}}>{snapshotMeta.original_path || '—'}</span>
      <strong>{t('metaHostname') || 'Machine'}</strong>
      <span>{snapshotMeta.hostname || '—'}{snapshotMeta.os ? ` (${snapshotMeta.os})` : ''}{snapshotMeta.vss_used ? ' · VSS' : ''}</span>
      <strong>{t('metaBackupTime') || 'Sauvegardé le'}</strong>
      <span>{snapshotMeta.backup_time || '—'}{snapshotMeta.client_version ? ` · client ${snapshotMeta.client_version}` : ''}</span>
    </div>
  )
}
