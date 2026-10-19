import { useTranslation } from '../../i18n/i18nContext'
import SearchResults from './SearchResults'

// File search across this backup ID's snapshots.
export default function SearchPanel({ r, hostname }) {
  const { t } = useTranslation()
  return (
    <div style={{
      marginTop: '16px',
      padding: '14px',
      border: '1px solid var(--nc-border)',
      borderRadius: '8px',
      backgroundColor: 'var(--nc-panel-head)'
    }}>
      <p style={{fontSize: '13px', color: 'var(--nc-text-dim)', marginTop: 0}}>
        {t('searchHint').replace('{prefix}', (r.restoreBackupId || hostname || '?'))}
      </p>

      <div style={{display: 'flex', gap: '12px', flexWrap: 'wrap', alignItems: 'flex-end'}}>
        <div className="form-group" style={{flex: '2 1 280px', margin: 0}}>
          <label>{t('searchQueryLabel')}</label>
          <input
            type="text"
            value={r.searchQuery}
            onChange={(e) => r.setSearchQuery(e.target.value)}
            placeholder={t('searchQueryPlaceholder')}
            onKeyDown={(e) => { if (e.key === 'Enter' && !r.searchRunning) r.handleSearch() }}
          />
        </div>
        <div className="form-group" style={{flex: '1 1 160px', margin: 0}}>
          <label>{t('searchModeLabel')}</label>
          <select value={r.searchMode} onChange={(e) => r.setSearchMode(e.target.value)}>
            <option value="name">{t('searchModeName')}</option>
            <option value="regex">{t('searchModeRegex')}</option>
            <option value="path">{t('searchModePath')}</option>
          </select>
        </div>
      </div>

      <div style={{display: 'flex', gap: '12px', flexWrap: 'wrap', alignItems: 'flex-end', marginTop: '10px'}}>
        <div className="form-group" style={{flex: '1 1 150px', margin: 0}}>
          <label>{t('searchFrom')}</label>
          <input type="date" value={r.searchFrom} onChange={(e) => r.setSearchFrom(e.target.value)} />
        </div>
        <div className="form-group" style={{flex: '1 1 150px', margin: 0}}>
          <label>{t('searchTo')}</label>
          <input type="date" value={r.searchTo} onChange={(e) => r.setSearchTo(e.target.value)} />
        </div>
        <label style={{display: 'flex', alignItems: 'center', gap: '8px', flex: '2 1 260px', fontSize: '14px'}}>
          <input
            type="checkbox"
            checked={r.searchAssembleMissing}
            onChange={(e) => r.setSearchAssembleMissing(e.target.checked)}
          />
          {t('searchAssembleMissing')}
        </label>
      </div>

      <div style={{marginTop: '12px', display: 'flex', gap: '8px', alignItems: 'center'}}>
        <button className="btn" type="button" onClick={r.handleSearch} disabled={r.searchRunning}>
          {r.searchRunning ? `⏳ ${t('searching')}` : `🔎 ${t('searchButton')}`}
        </button>
        {r.searchRunning && (
          <button className="btn btn-danger" type="button" onClick={r.handleCancelSearch}>
            ✖ {t('cancel')}
          </button>
        )}
        {r.searchRunning && (
          <span style={{fontSize: '13px', color: 'var(--nc-text-dim)'}}>
            {r.searchProgress.percent}% — {r.searchProgress.message}
          </span>
        )}
      </div>

      {r.searchResult && <SearchResults r={r} />}
    </div>
  )
}
