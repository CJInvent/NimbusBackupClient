import { useState } from 'react'
import usePolling from './usePolling'
import { GetActiveRuns, GetConnections, GetRecentRuns } from '../lib/bindings'

// Poll cadences. The live run moves; connections and history do not, and the
// service probes destinations on its own 60s sweep, so asking faster than that
// only costs round trips.
const ACTIVE_POLL_MS = 3000
const SLOW_POLL_MS = 30000

const RECENT_DAYS = 7

// What the status page shows, from three polled endpoints and nothing else:
//   /connections   what this machine can reach
//   /runs/active   what is running right now
//   /runs/recent   the last seven days
export default function useStatusPanel() {
  const [conns, setConns] = useState(null)
  const [active, setActive] = useState(null)
  const [recent, setRecent] = useState([])
  const [serviceUp, setServiceUp] = useState(true)

  // Fast loop: what is running.
  usePolling(async (alive) => {
    try {
      const resp = await GetActiveRuns()
      if (!alive()) return
      setServiceUp(true)
      const runs = (resp && resp.runs) || []
      setActive(runs.length ? runs[0] : null)
    } catch {
      if (alive()) setServiceUp(false)
    }
  }, ACTIVE_POLL_MS, !!GetActiveRuns)

  // Slow loop: connections and history. The service probes destinations on its
  // own 60s sweep, so polling this faster only costs round trips.
  usePolling(async (alive) => {
    try {
      if (GetConnections) {
        const c = await GetConnections()
        if (alive()) setConns(c)
      }
      if (GetRecentRuns) {
        const r = await GetRecentRuns(RECENT_DAYS)
        if (alive()) setRecent((r && r.runs) || [])
      }
    } catch {
      /* the service tile already says the service is not answering */
    }
  }, SLOW_POLL_MS, !!(GetConnections || GetRecentRuns))

  return { conns, active, recent, serviceUp }
}
