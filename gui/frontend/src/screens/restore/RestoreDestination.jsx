import { useTranslation } from '../../i18n/i18nContext'

// Alternate: destination + keep-tree toggle
export default function RestoreDestination({ r }) {
  const { t } = useTranslation()
  return (
    <>
      <div className="form-group">
        <label>{t('destinationPath')}</label>
        <div style={{display: 'flex', gap: '8px'}}>
          <input
            type="text"
            value={r.restoreDestPath}
            onChange={(e) => r.setRestoreDestPath(e.target.value)}
            placeholder="C:\Restore"
            style={{flex: 1}}
          />
          <button className="btn" onClick={r.handleBrowseRestoreDest} type="button">
            📁 {t('browse')}
          </button>
        </div>
      </div>
      <label style={{display: 'flex', alignItems: 'center', gap: '6px', marginBottom: '12px'}}>
        <input
          type="checkbox"
          checked={r.restoreKeepTree}
          onChange={(e) => r.setRestoreKeepTree(e.target.checked)}
        />
        {t('keepTreeLabel') || 'Conserver l\'arborescence d\'origine'}
        <span style={{fontSize: '12px', color: 'var(--nc-text-dim)'}}>
          {r.restoreKeepTree
            ? (t('keepTreeOnHint') || '(dest/Users/alice/doc.txt)')
            : (t('keepTreeOffHint') || '(dest/doc.txt — recommandé pour un fichier seul)')}
        </span>
      </label>
    </>
  )
}
