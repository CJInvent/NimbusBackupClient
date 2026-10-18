import { useState } from 'react'
import { useTranslation } from './i18n/i18nContext'
import StorageIdentityPanel from './components/StorageIdentityPanel'
import PathPicker from './components/PathPicker'

import useStatusMessage from './hooks/useStatusMessage'
import useControlServerForm from './hooks/useControlServerForm'
import useMachineContext from './hooks/useMachineContext'
import useAppData from './hooks/useAppData'
import useSecurity from './hooks/useSecurity'
import usePbsServers from './hooks/usePbsServers'
import useBackupForm from './hooks/useBackupForm'
import useScheduler from './hooks/useScheduler'
import useBackupRun from './hooks/useBackupRun'
import usePicker from './hooks/usePicker'
import useRestore from './hooks/useRestore'

import makeServerActions from './screens/setup/serverActions'
import makeSaveControlServer from './screens/setup/controlServerActions'
import makeBackupActions from './screens/backup/backupActions'
import makeJobActions from './screens/jobs/jobActions'
import makeHistoryActions from './screens/history/historyActions'

import Header from './screens/frame/Header'
import OrgSuspendedBanner from './screens/frame/OrgSuspendedBanner'
import ReleaseBanners from './screens/frame/ReleaseBanners'
import SecurityWarnings from './screens/frame/SecurityWarnings'
import Tabs from './screens/frame/Tabs'
import LockedView from './screens/status/LockedView'
import SetupScreen from './screens/setup/SetupScreen'
import BackupScreen from './screens/backup/BackupScreen'
import RestoreScreen from './screens/restore/RestoreScreen'
import AboutScreen from './screens/about/AboutScreen'

// The window: the hooks that hold the state and talk to the service, and the
// screens that render it (docs/V4-GUI-AUDIT.md section 4.11, step S2). A
// locked console gets the status page and nothing else.
function App() {
  const { t } = useTranslation()
  const [activeTab, setActiveTab] = useState('servers')
  const { status, showStatus } = useStatusMessage(t)
  const cpf = useControlServerForm()
  const { cpStatus, setCpStatus, readOnly, approveStorage } = useMachineContext(cpf.prefill)
  const f = useBackupForm()
  const app = useAppData(f.setBackupDirs)
  const security = useSecurity()
  const servers = usePbsServers()
  const jobs = useScheduler()
  const run = useBackupRun(showStatus, jobs.setJobHistory)
  const pick = usePicker()
  const r = useRestore({
    t, showStatus, config: app.config, hostname: app.hostname, systemInfo: app.systemInfo,
    defaultPBSID: servers.defaultPBSID, openPicker: pick.openPicker,
  })

  const srv = { ...servers, ...makeServerActions(servers, showStatus, t) }
  const cp = {
    cpStatus, cpForm: cpf.cpForm, setCpForm: cpf.setCpForm,
    saveControlServer: makeSaveControlServer({ cpForm: cpf.cpForm, setCpForm: cpf.setCpForm, setCpStatus }, showStatus, t),
  }
  const act = {
    ...makeBackupActions({ f, run, jobs, app }, showStatus, t),
    ...makeJobActions({ f, jobs, app, setActiveTab }, showStatus, t),
    ...makeHistoryActions({ f, app }, showStatus, t),
  }

  // null means "not asked yet" — render nothing rather than flashing the full
  // UI for a frame on a machine that turns out to be locked.
  if (readOnly === null) return null
  if (readOnly) {
    return <LockedView cpStatus={cpStatus} version={app.appVersion} hostname={app.config['backup-id']} />
  }

  return (
    <>
      <Header />

      <OrgSuspendedBanner cpStatus={cpStatus} />
      <ReleaseBanners status={cpStatus} />
      <StorageIdentityPanel status={cpStatus?.storage_identity} onApprove={approveStorage} />

      {pick.picker && (
        <PathPicker
          mode={pick.picker.mode}
          initialPath={pick.picker.initialPath}
          defaultFileName={pick.picker.defaultFileName}
          needBytes={pick.picker.needBytes}
          api={pick.pickerApi}
          onCancel={() => pick.closePicker('')}
          onConfirm={(path) => pick.closePicker(path)}
        />
      )}

      <div className="container">
        <SecurityWarnings warnings={security.securityWarnings} />

        <Tabs activeTab={activeTab} onSelect={setActiveTab} />

        <SetupScreen active={activeTab === 'servers'} srv={srv} cp={cp} config={app.config} appVersion={app.appVersion} status={status} />

        <BackupScreen active={activeTab === 'backup'} f={f} run={run} jobs={jobs} app={app} security={security} act={act} status={status} />

        <RestoreScreen
          active={activeTab === 'restore'}
          r={r}
          pbsServers={servers.pbsServers}
          defaultPBSID={servers.defaultPBSID}
          hostname={app.hostname}
          systemInfo={app.systemInfo}
          status={status}
        />

        <AboutScreen active={activeTab === 'about'} appVersion={app.appVersion} />
      </div>
    </>
  )
}

export default App
