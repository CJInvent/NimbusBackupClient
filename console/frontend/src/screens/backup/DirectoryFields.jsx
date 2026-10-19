import { useTranslation } from '../../i18n/i18nContext'

// A folder backup: the folders, and what to leave out.
//
// EXCLUSIONS BELONG TO FOLDER BACKUPS ONLY. They were rendered in the MACHINE
// branch and nowhere else -- exactly backwards. ExcludeList reaches
// backupDirectory -> backupReal -> archive.ExcludeList, which only the
// directory engine runs; machine_backup_windows.go contains no reference to
// exclusions at all, because an image backup is a block-level copy of a disk
// and there is nothing for a file pattern to act on. So the control was
// offered where it does nothing and hidden where it works. See
// docs/V4-JOB-TARGETS.md section 4 in NimbusControl.
export default function DirectoryFields({ backupDirs, setBackupDirs, excludeList, setExcludeList, config, setConfig }) {
  const { t } = useTranslation()
  return (
    <>
      <div className="form-group">
        <label>{t('directoriesToBackup')}</label>
        <textarea
          value={backupDirs}
          onChange={(e) => {
            setBackupDirs(e.target.value)
            // Update config.backupdir with first directory for compatibility
            const dirs = e.target.value.split('\n').map(d => d.trim()).filter(d => d)
            setConfig({...config, backupdir: dirs[0] || ''})
          }}
          rows="4"
          placeholder="C:\Data&#10;C:\Users&#10;D:\Documents"
        />
      </div>

      <div className="form-group">
        <label>{t('filesToExclude')}</label>
        <textarea
          value={excludeList}
          onChange={(e) => setExcludeList(e.target.value)}
          rows="4"
          placeholder="*.tmp&#10;*.log&#10;C:\Windows\Temp"
        />
      </div>
    </>
  )
}
