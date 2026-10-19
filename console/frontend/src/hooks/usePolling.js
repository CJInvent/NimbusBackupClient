import { useEffect } from 'react'

// Run poll at mount and then every intervalMs, until unmount. poll is handed
// a function that says whether the caller is still mounted, so an answer that
// arrives after unmount is dropped instead of written into a gone component.
// With enabled false nothing runs at all (the binding it would call is
// absent: a plain-browser dev session).
export default function usePolling(poll, intervalMs, enabled = true) {
  useEffect(() => {
    if (!enabled) return
    let alive = true
    const run = () => poll(() => alive)
    run()
    const id = setInterval(run, intervalMs)
    return () => { alive = false; clearInterval(id) }
  }, [])
}
