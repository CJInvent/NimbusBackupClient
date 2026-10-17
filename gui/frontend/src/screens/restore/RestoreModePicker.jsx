import { useTranslation } from '../../i18n/i18nContext'

// In place, or to another location. blocker is the translation key that says
// why in place is not possible (null: it is).
export default function RestoreModePicker({ r, blocker, blockerTooltip }) {
  const { t } = useTranslation()
  return (
    <div style={{
      display: 'flex',
      flexWrap: 'wrap',
      gap: '20px',
      marginBottom: '14px',
      padding: '10px 14px',
      border: '1px solid var(--nc-border)',
      borderRadius: '8px',
      backgroundColor: 'var(--nc-panel-head)'
    }}>
      <label
        title={blockerTooltip}
        style={{
          display: 'flex', alignItems: 'center', gap: '6px',
          opacity: blocker ? 0.5 : 1,
          cursor: blocker ? 'not-allowed' : 'pointer'
        }}
      >
        <input
          type="radio"
          name="restoreMode"
          value="original"
          checked={r.restoreMode === 'original'}
          disabled={!!blocker}
          onChange={() => r.setRestoreMode('original')}
        />
        <strong>{t('restoreModeInPlace') || 'Restaurer in-place'}</strong>
        {blocker && <span style={{fontSize: '11px', color: 'var(--nc-err)'}}> ({blockerTooltip})</span>}
      </label>
      <label style={{display: 'flex', alignItems: 'center', gap: '6px', cursor: 'pointer'}}>
        <input
          type="radio"
          name="restoreMode"
          value="alternate"
          checked={r.restoreMode !== 'original'}
          onChange={() => r.setRestoreMode('alternate_abs')}
        />
        <strong>{t('restoreModeAlternate') || 'Restaurer vers un autre emplacement'}</strong>
      </label>
    </div>
  )
}
