import { useTranslation } from '../../i18n/i18nContext'

export default function BackupTypeField({ backupType, setBackupType }) {
  const { t } = useTranslation()
  return (
    <div className="form-group">
      <label>{t('backupType')}</label>
      <select value={backupType} onChange={(e) => setBackupType(e.target.value)}>
        <option value="directory">📁 {t('backupTypeDirectory')}</option>
        <option value="machine">💾 {t('backupTypeMachine')}</option>
      </select>
    </div>
  )
}
