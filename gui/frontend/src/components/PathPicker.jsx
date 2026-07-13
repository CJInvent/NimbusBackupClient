import { useEffect, useState, useCallback } from 'react'
import { useTranslation } from '../i18n/i18nContext'

/**
 * PathPicker — an in-app folder / Save-As dialog.
 *
 * WHY: the Wails native dialogs take an uncatchable native COM fault in this
 * app — the whole process dies, tray icon included, with nothing logged. A
 * backup tool cannot ship a button that kills the process, so we render the
 * picker ourselves over plain Go filesystem enumeration (pathpicker.go). It
 * cannot fault, and it behaves the same in every process.
 *
 * mode='folder'  -> pick a destination folder; confirms with that folder path.
 * mode='save'    -> pick folder + filename; confirms with the joined path.
 *
 * Props: mode, initialPath, defaultFileName, needBytes (to show whether the
 * selection fits), onCancel(), onConfirm(path)
 */
export default function PathPicker({ mode = 'folder', initialPath = '', defaultFileName = '', needBytes = 0, api, onCancel, onConfirm }) {
  const { t } = useTranslation()
  const [drives, setDrives] = useState([])
  const [listing, setListing] = useState(null)
  const [fileName, setFileName] = useState(defaultFileName)
  const [newFolder, setNewFolder] = useState('')
  const [showNewFolder, setShowNewFolder] = useState(false)
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)

  // Windows paths use backslash; POSIX forward slash. Infer from what the
  // backend hands back rather than hardcoding, so this component is correct
  // on both (and on the Linux CI build).
  const sep = (listing && listing.path && listing.path.includes('\\')) ? '\\' : '/'
  const joinPath = (dir, name) => dir.endsWith(sep) ? dir + name : dir + sep + name

  const load = useCallback(async (path) => {
    setBusy(true)
    setError('')
    try {
      const l = await api.ListFolders(path || '')
      setListing(l)
    } catch (e) {
      setError(e && e.message ? e.message : String(e))
    } finally {
      setBusy(false)
    }
  }, [api])

  useEffect(() => {
    let alive = true
    ;(async () => {
      try {
        const d = await api.ListDrives()
        if (alive) setDrives(d || [])
      } catch (e) { /* drives are a convenience; the path field still works */ }
      const start = initialPath || (await api.DefaultSaveDir().catch(() => ''))
      if (alive) await load(start)
    })()
    return () => { alive = false }
  }, [api, initialPath, load])

  const freeOk = !listing || !needBytes || needBytes <= (listing.free_bytes || 0)

  const confirm = () => {
    if (!listing) return
    if (mode === 'save') {
      const name = (fileName || '').trim()
      if (!name) { setError(t('pickerNameRequired')); return }
      if (/[\\/:*?"<>|]/.test(name)) { setError(t('pickerNameInvalid')); return }
      onConfirm(joinPath(listing.path, name))
      return
    }
    onConfirm(listing.path)
  }

  const createFolder = async () => {
    const name = (newFolder || '').trim()
    if (!name || !listing) return
    try {
      const created = await api.CreateFolder(listing.path, name)
      setNewFolder('')
      setShowNewFolder(false)
      await load(created)
    } catch (e) {
      setError(e && e.message ? e.message : String(e))
    }
  }

  const fmt = (b) => {
    if (!b && b !== 0) return '—'
    const u = ['B', 'KB', 'MB', 'GB', 'TB']
    let i = 0, n = Number(b)
    while (n >= 1024 && i < u.length - 1) { n /= 1024; i++ }
    return `${n.toFixed(n < 10 && i > 0 ? 1 : 0)} ${u[i]}`
  }

  return (
    <div className="nc-modal-backdrop" onClick={(e) => { if (e.target === e.currentTarget) onCancel() }}>
      <div className="nc-modal">
        <div className="nc-modal-head">
          {mode === 'save' ? t('pickerSaveTitle') : t('pickerFolderTitle')}
        </div>

        <div className="nc-picker-body">
          {/* Drive rail — free space shown at the moment of choosing */}
          <div className="nc-picker-drives">
            {drives.map(d => (
              <button
                key={d.path}
                className={'nc-drive' + (listing && listing.path && listing.path.toUpperCase().startsWith(d.path.toUpperCase()) ? ' active' : '')}
                disabled={!d.ready}
                onClick={() => load(d.path)}
                title={d.ready ? `${fmt(d.free_bytes)} ${t('pickerFree')}` : t('pickerNotReady')}
              >
                <span className="nc-drive-name">💽 {d.path}{d.label ? ` (${d.label})` : ''}</span>
                {d.ready && <span className="nc-drive-space">{fmt(d.free_bytes)} {t('pickerFree')}</span>}
              </button>
            ))}
          </div>

          {/* Folder pane */}
          <div className="nc-picker-main">
            <div className="nc-picker-path">
              <button className="btn" disabled={!listing || !listing.parent} onClick={() => load(listing.parent)}>⬆ {t('pickerUp')}</button>
              <input
                type="text"
                className="mono"
                value={listing ? listing.path : ''}
                onChange={(e) => setListing(l => ({ ...(l || {}), path: e.target.value }))}
                onKeyDown={(e) => { if (e.key === 'Enter') load(e.currentTarget.value) }}
                spellCheck={false}
              />
              <button className="btn" onClick={() => setShowNewFolder(v => !v)}>➕ {t('pickerNewFolder')}</button>
            </div>

            {showNewFolder && (
              <div className="nc-picker-path">
                <input
                  type="text"
                  placeholder={t('pickerNewFolderName')}
                  value={newFolder}
                  onChange={(e) => setNewFolder(e.target.value)}
                  onKeyDown={(e) => { if (e.key === 'Enter') createFolder() }}
                  autoFocus
                />
                <button className="btn btn-primary" onClick={createFolder}>{t('pickerCreate')}</button>
              </div>
            )}

            <div className="nc-picker-list">
              {busy && <div className="hint-text" style={{ padding: '8px' }}>{t('pickerLoading')}</div>}
              {!busy && listing && (listing.folders || []).length === 0 && (
                <div className="hint-text" style={{ padding: '8px' }}>{t('pickerNoSubfolders')}</div>
              )}
              {!busy && listing && (listing.folders || []).map(f => (
                <div key={f.path} className="nc-picker-row" onDoubleClick={() => load(f.path)} onClick={() => load(f.path)}>
                  📁 <span>{f.name}</span>
                </div>
              ))}
            </div>

            {mode === 'save' && (
              <div className="nc-picker-path">
                <label style={{ margin: 0, minWidth: '70px' }}>{t('pickerFileName')}</label>
                <input
                  type="text"
                  value={fileName}
                  onChange={(e) => setFileName(e.target.value)}
                  onKeyDown={(e) => { if (e.key === 'Enter') confirm() }}
                  spellCheck={false}
                />
              </div>
            )}
          </div>
        </div>

        {/* Space readout: the user finds out BEFORE committing, not after */}
        {listing && (
          <div className={'nc-picker-space' + (freeOk ? '' : ' bad')}>
            {t('pickerFreeOnDrive')}: <strong>{fmt(listing.free_bytes)}</strong>
            {!!needBytes && <> · {t('pickerNeeded')}: <strong>{fmt(needBytes)}</strong></>}
            {!freeOk && <> — <strong>{t('pickerWontFit')}</strong></>}
            {listing.writable === false && <> — <strong>{t('pickerNotWritable')}</strong></>}
          </div>
        )}

        {error && <div className="nc-picker-error">❌ {error}</div>}

        <div className="nc-modal-foot">
          <button className="btn" onClick={onCancel}>{t('cancel')}</button>
          <button
            className="btn btn-primary"
            disabled={!listing || !freeOk || listing.writable === false}
            onClick={confirm}
          >
            {mode === 'save' ? t('pickerSave') : t('pickerChoose')}
          </button>
        </div>
      </div>
    </div>
  )
}
