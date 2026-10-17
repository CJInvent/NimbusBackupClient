import { useTranslation } from '../../i18n/i18nContext'
import { formatBytes, fmtRate, fmtEta } from '../../lib/format'

const stat = {fontSize: '13px', color: 'var(--nc-text-dim)'}

// The run in flight: percentage, ETA, rate, data, chunks, folder, phase.
export default function BackupProgressCard({ progress, backupStats, backupPhase, status }) {
  const { t } = useTranslation()
  return (
    <div style={{marginTop: '20px', marginBottom: '20px', padding: '15px', backgroundColor: 'var(--nc-panel-head)', borderRadius: '8px', border: '1px solid var(--nc-border)'}}>
      <div style={{display: 'flex', justifyContent: 'space-between', marginBottom: '10px'}}>
        <strong style={{fontSize: '15px'}}>📊 {t('backupProgress')}</strong>
        <span style={{fontSize: '18px', fontWeight: 'bold', color: 'var(--nc-accent)'}}>{progress}%</span>
      </div>

      <div className="progress" style={{height: '30px', marginBottom: '12px'}}>
        <div
          className="progress-bar"
          style={{
            width: `${progress}%`,
            fontSize: '14px',
            lineHeight: '30px',
            transition: 'width 0.3s ease',
            fontWeight: 'bold'
          }}
        >
          {progress}%
        </div>
      </div>

      <div style={{display: 'grid', gridTemplateColumns: '1fr 1fr', gap: '10px', marginBottom: '10px'}}>
        {backupStats.eta !== null && (
          <div style={stat}>
            ⏱️ <strong>{t('timeRemaining')}</strong> ≥ {fmtEta(backupStats.eta)}
          </div>
        )}
        {backupStats.byteRate > 0 && (
          <div style={stat}>
            ⚡ <strong>{t('speed')}</strong> {fmtRate(backupStats.byteRate)}
          </div>
        )}
        {backupStats.startTime && (
          <div style={stat}>
            ⏰ <strong>{t('elapsedTime')}</strong> {Math.floor((Date.now() - backupStats.startTime) / 1000)}s
          </div>
        )}
        {backupStats.bytesDone > 0 && (
          <div style={stat}>
            📦 <strong>{t('dataLabel')}</strong> {formatBytes(backupStats.bytesDone)}
            {backupStats.bytesTotal > 0 ? ` / ${formatBytes(backupStats.bytesTotal)}` : ''}
          </div>
        )}
        {(backupStats.newChunks > 0 || backupStats.reusedChunks > 0) && (
          <div style={stat}>
            🧩 <strong>{t('chunksLabel')}</strong> {backupStats.newChunks} {t('chunksNew')} · {backupStats.reusedChunks} {t('chunksReused')}
            {backupStats.failedChunks > 0 ? (
              <span style={{color: 'var(--nc-err)', fontWeight: 'bold'}}> · {backupStats.failedChunks} {t('chunksFailed')}</span>
            ) : ''}
          </div>
        )}
        {backupStats.currentDir && (
          <div style={{fontSize: '13px', color: 'var(--nc-text-dim)', gridColumn: '1 / -1', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap'}}>
            📁 <strong>{t('folderLabel')}</strong> {backupStats.currentDir}
          </div>
        )}
        {backupPhase && (
          <div style={{fontSize: '13px', color: 'var(--nc-text-dim)', gridColumn: '1 / -1'}}>
            🔄 {backupPhase}
          </div>
        )}
      </div>

      {status.message && status.type === 'info' && (
        <div style={{marginTop: '10px', padding: '8px', backgroundColor: 'var(--nc-panel)', borderRadius: '4px', fontSize: '13px', color: 'var(--nc-text-dim)', border: '1px solid var(--nc-border-soft)'}}>
          {status.message}
        </div>
      )}
    </div>
  )
}
