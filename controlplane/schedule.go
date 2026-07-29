package controlplane

import "time"

// NextAligned is THE shared scheduling primitive for every server-assigned,
// periodic client task (today: the check-in loop in loop.go, and the GUI's
// PBS-connectivity poller in gui/controlplane_pbspoll.go). Any future
// recurring, server-coordinated task should call this rather than rolling
// its own ticker.
//
// It answers one question: given a server-assigned (intervalSeconds,
// offsetSeconds) pair, how long until the next tick? Ticks land on
// wall-clock instants satisfying `unixTime % intervalSeconds == offsetSeconds`
// -- an epoch-aligned grid, not "intervalSeconds after whenever I last ran".
//
// WHY EPOCH-ALIGNED, NOT "SLEEP N SECONDS FROM NOW": a naive loop that just
// sleeps intervalSeconds after each run resynchronizes every agent that
// happens to (re)start around the same moment -- a mass Windows Update
// reboot puts an entire fleet back on an identical clock, defeating any
// server-side stagger the moment it matters most. Aligning to the epoch
// instead means every agent lands on the SAME global grid regardless of
// when its process happens to start: a restart just resumes at the correct
// point in a schedule that was never reset, not the start of a new one.
//
// WHY OFFSET COMES FROM THE SERVER, NOT COMPUTED LOCALLY: the client could
// derive a stable per-agent offset from its own agent ID with no server
// involvement at all -- but the server is the authoritative source for
// polling configuration (NimbusControl Nimbus\Agents\PollSchedule computes
// the identical value server-side and hands it back at check-in), so a
// future rebalancing strategy smarter than a hash can change server-side
// with no client release required.
func NextAligned(now time.Time, intervalSeconds, offsetSeconds int) time.Duration {
	if intervalSeconds <= 0 {
		intervalSeconds = 1
	}
	interval := int64(intervalSeconds)
	offset := int64(offsetSeconds) % interval
	if offset < 0 {
		offset += interval
	}
	nowUnix := now.Unix()
	// k is the smallest integer such that k*interval + offset > nowUnix.
	// Integer division truncates toward zero, which is safe here because
	// nowUnix-offset is always a huge positive number (real epoch time
	// minus a small offset), never negative.
	k := (nowUnix-offset)/interval + 1
	next := k*interval + offset
	return time.Duration(next-nowUnix) * time.Second
}
