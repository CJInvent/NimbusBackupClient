import { useState, useEffect } from 'react'
import { GetScheduledJobs, GetJobHistory } from '../lib/bindings'

// Local scheduled jobs and the job history, loaded at mount; the history is
// refreshed every 10 s to update the status of running jobs.
export default function useScheduler() {
  const [scheduledJobs, setScheduledJobs] = useState([])
  const [jobHistory, setJobHistory] = useState([])

  useEffect(() => {
    const loadSchedulerData = async () => {
      try {
        if (GetScheduledJobs) {
          const jobs = await GetScheduledJobs()
          setScheduledJobs(jobs || [])
        }

        if (GetJobHistory) {
          const history = await GetJobHistory()
          setJobHistory(history || [])
        }
      } catch (err) {
        console.error('Failed to load scheduler data:', err)
      }
    }

    loadSchedulerData()

    // Refresh history every 10 seconds to update status of running jobs
    const intervalId = setInterval(() => {
      if (GetJobHistory) {
        GetJobHistory().then(history => {
          setJobHistory(history || [])
        }).catch(err => {
          console.error('Failed to refresh job history:', err)
        })
      }
    }, 10000) // 10 seconds

    return () => clearInterval(intervalId)
  }, [])

  return { scheduledJobs, setScheduledJobs, jobHistory, setJobHistory }
}
