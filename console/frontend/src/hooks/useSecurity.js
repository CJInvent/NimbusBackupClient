import { useState, useEffect } from 'react'
import { GetSecurityWarnings, GetExchangeStatus, QueryExchangeLogMode } from '../lib/bindings'

// Security-posture warnings (the banner above the tabs) and Exchange
// detection (the backup form's Exchange section), read once at mount.
export default function useSecurity() {
  const [securityWarnings, setSecurityWarnings] = useState([])
  const [exchangeStatus, setExchangeStatus] = useState({installed:false, version:"", aware:false, highlight_setting:false, log_truncation:false, highlight_truncation:false})
  const [exchangeLogMode, setExchangeLogMode] = useState({queried:false, logs_accumulate:false, detail:""})

  useEffect(() => {
    if (GetSecurityWarnings) {
      GetSecurityWarnings().then(w => setSecurityWarnings(w || [])).catch(() => {})
    }
    if (GetExchangeStatus) {
      GetExchangeStatus().then(st => {
        setExchangeStatus(st || {})
        if (st && st.installed && QueryExchangeLogMode) {
          QueryExchangeLogMode().then(m => setExchangeLogMode(m || {})).catch(() => {})
        }
      }).catch(() => {})
    }
  }, [])

  return { securityWarnings, exchangeStatus, exchangeLogMode }
}
