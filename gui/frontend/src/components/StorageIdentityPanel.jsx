import { useState } from 'react'
import { useTranslation } from '../i18n/i18nContext'

export default function StorageIdentityPanel({ status, onChanged, readOnly = false }) {
  const { t } = useTranslation()
  const [selected, setSelected] = useState([])
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)
  if (!status) return null
  const devices = status.devices || []
  const dashboard = /^https?:\/\//i.test(status.dashboard_url || '')
    ? status.dashboard_url.replace(/\/$/, '') + '/agents/' + Number(status.agent_id || 0) : ''
  const approve = async () => {
    setBusy(true); setError('')
    try {
      const bindings = devices.filter(d => selected.includes(d.boot ? 'boot' : 'device:' + d.id))
        .map(d => ({ target: d.boot ? 'boot' : 'device:' + d.id, device: d }))
      await window.go.main.App.ApproveStorageIdentity({
        revision: (status.revision || 0) + 1, observation: status.observation, bindings
      })
      onChanged(await window.go.main.App.GetStorageIdentityStatus())
      setSelected([])
    } catch (e) { setError(String(e)) } finally { setBusy(false) }
  }
  return <details className="card" open={!!status.error} style={{overflowWrap: 'anywhere'}}>
    <summary>{status.error ? t('storageError') : t('storageTitle')}</summary>
    {status.error && <p role="alert">{status.error}</p>}
    <p>{t('storageReview')}</p>
    {(status.bindings || []).length > 0 && <details>
      <summary>{t('storagePrevious')}</summary>
      {status.bindings.map(b => <p key={b.target}><code>{b.device.id}</code> / <code>{b.device.disk_id}</code></p>)}
    </details>}
    {devices.map(d => {
      const target = d.boot ? 'boot' : 'device:' + d.id
      return <div key={d.id + d.path}>
        <label>
          {!status.joined && !readOnly && <input type="checkbox" checked={selected.includes(target)} disabled={busy}
            onChange={e => setSelected(e.target.checked ? [...selected, target] : selected.filter(v => v !== target))} />}
          {d.boot ? t('storageBoot') : t('storageDevice')} <code>{d.id}</code>
        </label>
        <details><summary>{t('storageEvidence')}</summary>
          <p><code>{d.disk_id}</code></p><p><code>{d.path}</code> · {t('size')}: {Number(d.size_bytes).toLocaleString()}</p>
          {(d.partitions || []).map(p => <p key={p.id}><code>{p.id}</code></p>)}
        </details>
      </div>
    })}
    {status.joined ? <p>{dashboard ? <a href={dashboard} target="_blank" rel="noopener noreferrer">{t('storageDashboard')}</a> : t('storageDashboard')}</p> :
      !readOnly && <button onClick={approve} disabled={busy || selected.length === 0}>{t('storageApprove')}</button>}
    {error && <p role="alert">{error}</p>}
  </details>
}
