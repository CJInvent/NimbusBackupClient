import { useState, useEffect } from 'react'
import { ListPhysicalDisks } from '../lib/bindings'

// The backup form: what to back up (folders and exclusions, or disks), how
// (now or on a schedule), and which scheduled job is being edited.
export default function useBackupForm() {
  const [backupType, setBackupType] = useState('directory')
  const [backupDirs, setBackupDirs] = useState('')
  const [selectedDrives, setSelectedDrives] = useState([])
  const [physicalDisks, setPhysicalDisks] = useState([])
  const [disksLoading, setDisksLoading] = useState(false)
  const [disksError, setDisksError] = useState('')
  const [excludeList, setExcludeList] = useState('')
  // Scheduling states
  const [backupMode, setBackupMode] = useState('oneshot') // 'oneshot' or 'scheduled'
  const [scheduleTime, setScheduleTime] = useState('02:00')
  const [runAtStartup, setRunAtStartup] = useState(false)
  const [editingJobId, setEditingJobId] = useState(null) // Track which job is being edited

  // What exclusions actually apply, given the backup type.
  //
  // HIDING THE FIELD IS NOT ENOUGH. The value lives in component state, so a
  // list typed for a folder backup and then switched to an image backup would
  // still be SENT -- ignored by the engine, but stored on a scheduled job and
  // read by the next person as though it were in force. An image backup is a
  // block-level copy of a disk; there is nothing for a file pattern to act on.
  // See docs/V4-JOB-TARGETS.md section 4 in NimbusControl.
  const excludesForType = () =>
    backupType === 'directory'
      ? excludeList.split('\n').filter(l => l.trim())
      : []

  // Load physical disks when switching to machine (full-volume) mode.
  // Retries on each re-entry to the Machine tab (effect re-runs on backupType).
  useEffect(() => {
    if (backupType !== 'machine' || !ListPhysicalDisks) return
    if (physicalDisks.length > 0 || disksLoading) return
    setDisksError('')
    setDisksLoading(true)
    ListPhysicalDisks().then(disks => {
      setPhysicalDisks(disks || [])
      // Boot Drive is semantic; never assume the first enumerated disk.
      if (disks && disks.length > 0 && selectedDrives.length === 0) {
        const boot = disks.find(d => d.target === 'boot')
        if (boot) setSelectedDrives(['boot'])
      }
    }).catch(err => {
      setDisksError(String(err))
    }).finally(() => {
      setDisksLoading(false)
    })
  }, [backupType])

  return {
    backupType, setBackupType, backupDirs, setBackupDirs, selectedDrives, setSelectedDrives,
    physicalDisks, disksLoading, disksError, excludeList, setExcludeList,
    backupMode, setBackupMode, scheduleTime, setScheduleTime, runAtStartup, setRunAtStartup,
    editingJobId, setEditingJobId, excludesForType,
  }
}
