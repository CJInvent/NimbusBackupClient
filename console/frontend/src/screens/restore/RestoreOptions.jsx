import { useTranslation } from '../../i18n/i18nContext'

// Unified restore options. Greyed options carry their reason as the
// tooltip — the matrix (restoreMatrix) is the single source of truth for
// what applies to this source.
export default function RestoreOptions({ r, isInPlace }) {
  const { t } = useTranslation()
  const m = r.restoreMatrix
  return (
    <div style={{display: 'flex', flexWrap: 'wrap', gap: '12px', marginBottom: '12px'}}>
      <label style={{display: 'flex', alignItems: 'center', gap: '6px', opacity: isInPlace ? 0.5 : 1}}
             title={isInPlace ? (t('overwriteForcedInPlace') || '') : ''}>
        <input
          type="checkbox"
          checked={isInPlace ? true : r.restoreOptions.overwrite}
          disabled={isInPlace}
          onChange={(e) => r.setRestoreOptions(o => ({...o, overwrite: e.target.checked}))}
        />
        {t('optionOverwrite')}
      </label>
      <label style={{display: 'flex', alignItems: 'center', gap: '6px', opacity: m.mtime.on ? 1 : 0.5}}
             title={m.mtime.why}>
        <input
          type="checkbox"
          disabled={!m.mtime.on}
          checked={m.mtime.on && r.restoreOptions.timestamps}
          onChange={(e) => r.setRestoreOptions(o => ({...o, timestamps: e.target.checked}))}
        />
        {t('optionTimestamps')}
      </label>
      <label style={{display: 'flex', alignItems: 'center', gap: '6px', opacity: m.acl.on ? 1 : 0.5}}
             title={m.acl.why}>
        <input
          type="checkbox"
          disabled={!m.acl.on}
          checked={m.acl.on && r.restoreOptions.acls}
          onChange={(e) => r.setRestoreOptions(o => ({...o, acls: e.target.checked}))}
        />
        {t('optionACLs')} <span style={{fontSize: '11px', color: 'var(--nc-warn)'}}>{m.acl.on ? t('betaTag') : ''}</span>
      </label>
      <label style={{display: 'flex', alignItems: 'center', gap: '6px', opacity: m.ads.on ? 1 : 0.5}}
             title={m.ads.why}>
        <input
          type="checkbox"
          disabled={!m.ads.on}
          checked={m.ads.on && r.restoreOptions.ads}
          onChange={(e) => r.setRestoreOptions(o => ({...o, ads: e.target.checked}))}
        />
        {t('optionADS')}
      </label>
      <label style={{display: 'flex', alignItems: 'center', gap: '6px'}}
             title={t('zipHint')}>
        <input
          type="checkbox"
          checked={r.packageAsZip}
          onChange={(e) => r.setPackageAsZip(e.target.checked)}
        />
        {t('optionZip')}
      </label>
    </div>
  )
}
