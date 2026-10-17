import { useTranslation } from '../../i18n/i18nContext'

// In-place: warning banner + cross-host override
export default function InPlaceWarning({ r, crossHost, hostname }) {
  const { t } = useTranslation()
  return (
    <div style={{
      marginBottom: '14px',
      padding: '10px 14px',
      border: '1px solid color-mix(in srgb, var(--nc-err) 45%, var(--nc-border))',
      backgroundColor: 'var(--nc-err-bg)',
      borderRadius: '8px',
      fontSize: '13px'
    }}>
      <div style={{color: 'var(--nc-err)', marginBottom: '6px'}}>
        ⚠️ {t('inPlaceWarning').replace('{path}', r.snapshotMeta?.original_path || '?')}
      </div>
      {crossHost && (
        <label style={{display: 'flex', alignItems: 'center', gap: '6px', color: 'var(--nc-text)'}}>
          <input
            type="checkbox"
            checked={r.restoreAllowCrossHost}
            onChange={(e) => r.setRestoreAllowCrossHost(e.target.checked)}
          />
          {t('crossHostOverride')
            .replace('{src}', r.snapshotMeta?.hostname || '?')
            .replace('{dst}', hostname || '?')}
        </label>
      )}
    </div>
  )
}
