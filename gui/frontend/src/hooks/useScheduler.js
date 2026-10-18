import { useState, useEffect } from 'react'
import { GetScheduledJobs, GetRecentRuns } from '../lib/bindings'
import { HISTORY_DAYS, runsToHistory } from '../lib/history'

// Local scheduled jobs, loaded at mount, and the history: the service's runs
// (lib/history.js), refreshed every 10 s to update the status of running ones.
export default function useScheduler() {
  const [scheduledJobs, setScheduledJobs] = useState([])
  const [jobHistory, setJobHistory] = useState([])

  useEffect(() => {
    const loadHistory = async () => {
      if (GetRecentRuns) setJobHistory(runsToHistory(await GetRecentRuns(HISTORY_DAYS)))
    }

    const loadSchedulerData = async () => {
      try {
        if (GetScheduledJobs) {
          const jobs = await GetScheduledJobs()
          setScheduledJobs(jobs || [])
        }
        await loadHistory()
      } catch (err) {
        console.error('Failed to load scheduler data:', err)
      }
    }

    loadSchedulerData()

    // Refresh history every 10 seconds to update status of running jobs
    const intervalId = setInterval(() => {
      loadHistory().catch(err => {
        console.error('Failed to refresh job history:', err)
      })
    }, 10000) // 10 seconds

    return () => clearInterval(intervalId)
  }, [])

  return { scheduledJobs, setScheduledJobs, jobHistory, setJobHistory }
}
