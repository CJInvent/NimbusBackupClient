import { useState } from 'react'
import { ListDrives, ListFolders, CreateFolder, DefaultSaveDir } from '../lib/bindings'

// The in-app path picker, as a promise — call sites read like the old dialog
// API but nothing native is involved, so nothing can fault.
export default function usePicker() {
  const [picker, setPicker] = useState(null)  // {mode, initialPath, defaultFileName, needBytes, resolve}
  const pickerApi = { ListDrives, ListFolders, CreateFolder, DefaultSaveDir }
  const openPicker = (opts) => new Promise((resolve) => {
    setPicker({ ...opts, resolve })
  })
  const closePicker = (value) => {
    setPicker(p => { if (p && p.resolve) p.resolve(value); return null })
  }
  return { picker, pickerApi, openPicker, closePicker }
}
