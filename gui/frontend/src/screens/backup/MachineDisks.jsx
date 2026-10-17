import { useTranslation } from '../../i18n/i18nContext'

// A machine (disk-image) backup: which disks.
export default function MachineDisks({ physicalDisks, disksLoading, disksError, selectedDrives, setSelectedDrives }) {
  const { t } = useTranslation()
  return (
    <>
      <div className="info-box" style={{backgroundColor: 'var(--nc-warn-bg)', borderColor: 'var(--nc-warn-border)'}}>
        ⚠️ <strong>{t('machineBackupWarning')}</strong><br/>
        {t('machineBackupWarningHint')}
      </div>

      <div className="form-group">
        <label>{t('physicalDisksToBackup')}</label>
        {disksLoading ? (
          <div style={{padding: '10px', backgroundColor: 'var(--nc-panel-head)', borderRadius: '4px'}}>
            🔍 {t('loadingDisks')}
          </div>
        ) : disksError ? (
          <div style={{padding: '10px', backgroundColor: 'var(--nc-err-bg)', borderRadius: '4px'}}>
            ❌ {t('diskDetectionError')}: {disksError}
          </div>
        ) : physicalDisks.length === 0 ? (
          <div style={{padding: '10px', backgroundColor: 'var(--nc-warn-bg)', borderRadius: '4px'}}>
            ⚠️ {t('noDisksDetected')}
          </div>
        ) : (
          <div style={{display: 'flex', flexDirection: 'column', gap: '8px'}}>
            {physicalDisks.map(disk => (
              <label key={disk.path} style={{display: 'flex', alignItems: 'center', gap: '8px'}}>
                <input
                  type="checkbox"
                  checked={selectedDrives.includes(disk.target)}
                  onChange={(e) => {
                    if (e.target.checked) {
                      setSelectedDrives([...selectedDrives, disk.target])
                    } else {
                      setSelectedDrives(selectedDrives.filter(d => d !== disk.target))
                    }
                  }}
                />
                {disk.target === 'boot' ? t('storageBoot') : t('storageDevice')} {disk.sizeText}
              </label>
            ))}
          </div>
        )}
      </div>

    </>
  )
}
