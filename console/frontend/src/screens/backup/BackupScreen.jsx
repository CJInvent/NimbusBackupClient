import { useTranslation } from '../../i18n/i18nContext'
import BackupTypeField from './BackupTypeField'
import BackupModeField from './BackupModeField'
import ScheduleOptions from './ScheduleOptions'
import DirectoryFields from './DirectoryFields'
import MachineDisks from './MachineDisks'
import BackupSettings from './BackupSettings'
import BackupProgressCard from './BackupProgressCard'
import BackupActions from './BackupActions'
import ScheduledJobs from '../jobs/ScheduledJobs'
import JobHistory from '../history/JobHistory'
import StatusNotice from '../frame/StatusNotice'

// The Backup tab: the form (f: useBackupForm), the run in flight (run:
// useBackupRun), the local scheduled jobs and the history (jobs:
// useScheduler). act holds the backup, job and history actions.
export default function BackupScreen({ active, f, run, jobs, app, security, act, status }) {
  const { t } = useTranslation()
  return (
    <div className={`tab-content ${active ? 'active' : ''}`}>
      <h2>{t('backupTitle')}</h2>

      <BackupTypeField backupType={f.backupType} setBackupType={f.setBackupType} />

      {/* Backup Mode Toggle */}
      <BackupModeField backupMode={f.backupMode} setBackupMode={f.setBackupMode} />

      {/* Scheduling Options */}
      {f.backupMode === 'scheduled' && (
        <ScheduleOptions
          editingJobId={f.editingJobId}
          scheduleTime={f.scheduleTime}
          setScheduleTime={f.setScheduleTime}
          runAtStartup={f.runAtStartup}
          setRunAtStartup={f.setRunAtStartup}
        />
      )}

      {f.backupType === 'directory' ? (
        <DirectoryFields
          backupDirs={f.backupDirs}
          setBackupDirs={f.setBackupDirs}
          excludeList={f.excludeList}
          setExcludeList={f.setExcludeList}
          config={app.config}
          setConfig={app.setConfig}
        />
      ) : (
        <MachineDisks
          physicalDisks={f.physicalDisks}
          disksLoading={f.disksLoading}
          disksError={f.disksError}
          selectedDrives={f.selectedDrives}
          setSelectedDrives={f.setSelectedDrives}
        />
      )}

      <BackupSettings
        config={app.config}
        setConfig={app.setConfig}
        exchangeStatus={security.exchangeStatus}
        exchangeLogMode={security.exchangeLogMode}
      />

      {run.progress > 0 && run.progress < 100 && (
        <BackupProgressCard progress={run.progress} backupStats={run.backupStats} backupPhase={run.backupPhase} status={status} />
      )}

      <BackupActions
        backupMode={f.backupMode}
        progress={run.progress}
        editingJobId={f.editingJobId}
        onStart={act.handleStartBackup}
        onStop={act.handleStopBackup}
        onCancelEdit={act.handleCancelJobEdit}
      />

      {/* Scheduled Jobs List */}
      {f.backupMode === 'scheduled' && jobs.scheduledJobs.length > 0 && (
        <ScheduledJobs scheduledJobs={jobs.scheduledJobs} onEdit={act.handleEditJob} onDelete={act.handleDeleteJob} />
      )}

      {/* Job History */}
      {jobs.jobHistory.length > 0 && (
        <JobHistory jobHistory={jobs.jobHistory} onRerun={act.handleRerun} />
      )}

      <StatusNotice status={status} show={active} />
    </div>
  )
}
