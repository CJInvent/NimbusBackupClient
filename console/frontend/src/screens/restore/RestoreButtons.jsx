import { useTranslation } from '../../i18n/i18nContext'

// Restore (or download the ZIP), and Cancel while one runs.
export default function RestoreButtons({ r, isInPlace, crossHost }) {
  const { t } = useTranslation()
  return (
    <div style={{display: 'flex', gap: '10px', alignItems: 'center'}}>
      <button
        className="btn btn-primary"
        onClick={r.packageAsZip ? r.handleDownloadSelection : r.handleRestoreSnapshot}
        disabled={r.restoreLoading || r.downloading || (r.packageAsZip && r.selectedPaths.size === 0) || (isInPlace && crossHost && !r.restoreAllowCrossHost)}
        title={r.packageAsZip ? t('zipButtonHint') : ''}
      >
        {r.restoreLoading || r.downloading ? `⏳ ${t('restoring')}` : `▶️ ${t('restore')}`}
      </button>
      {(r.restoreLoading || r.downloading) && (
        <button className="btn btn-danger" onClick={r.handleCancelRestore}>
          ✖ {t('cancel')}
        </button>
      )}
    </div>
  )
}
