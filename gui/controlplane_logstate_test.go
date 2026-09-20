package main

import "testing"

// The check-in callbacks are LEVEL-TRIGGERED: the server sends policy and the
// PBS poll schedule on every check-in so a retune propagates within one cycle.
// Logging each arrival wrote the same two lines every two minutes -- roughly
// 1,400 a day between them -- which is how a service log stops being read.
//
// These assert the property that fixes it (a change logs, a repeat does not)
// rather than the log call itself, because what matters is the decision, and
// a test that captured log output would pass just as happily if the decision
// were made in the wrong place.

func TestPolicyLogsOnChangeOnly(t *testing.T) {
	resetCheckinLogState()

	if !policyLogChanged(false) {
		t.Fatal("the first value after a reset must log: otherwise a restart leaves no record of what this agent is configured with")
	}
	if policyLogChanged(false) {
		t.Fatal("an unchanged policy must not log again -- this is the every-two-minutes line")
	}
	if !policyLogChanged(true) {
		t.Fatal("a changed policy must log")
	}
	if policyLogChanged(true) {
		t.Fatal("...and then settle")
	}
	if !policyLogChanged(false) {
		t.Fatal("changing back is a change too")
	}
}

func TestPollScheduleLogsOnChangeOnly(t *testing.T) {
	resetCheckinLogState()

	if !pollScheduleLogChanged(300, 47) {
		t.Fatal("the first schedule after a reset must log")
	}
	if pollScheduleLogChanged(300, 47) {
		t.Fatal("an unchanged schedule must not log again")
	}
	if !pollScheduleLogChanged(300, 48) {
		t.Fatal("a changed offset alone is a change: a fleet re-spread moves offsets, not intervals")
	}
	if !pollScheduleLogChanged(600, 48) {
		t.Fatal("a changed interval alone is a change")
	}
	if pollScheduleLogChanged(600, 48) {
		t.Fatal("...and then settles")
	}
}

// ZERO IS A REAL VALUE, not "unset". Comparing against a zero-valued int
// would silently swallow the first genuine (0, 0) schedule, and the agent
// would poll on a schedule that appears nowhere in its log. The sentinel is
// what keeps that honest, so it gets its own test.
func TestPollScheduleZeroIsLoggedAfterReset(t *testing.T) {
	resetCheckinLogState()

	if !pollScheduleLogChanged(0, 0) {
		t.Fatal("a zero schedule after a reset must log -- zero is a value, not an absence")
	}
	if pollScheduleLogChanged(0, 0) {
		t.Fatal("...but still only once")
	}
}

// A restart must re-log, so that "what is this agent configured with" is
// answerable from the log after every restart rather than only from whichever
// run first set the value.
func TestResetMakesTheNextCheckinLogAgain(t *testing.T) {
	resetCheckinLogState()
	policyLogChanged(true)
	pollScheduleLogChanged(300, 47)

	resetCheckinLogState()

	if !policyLogChanged(true) {
		t.Fatal("after a restart the same policy must log again")
	}
	if !pollScheduleLogChanged(300, 47) {
		t.Fatal("after a restart the same schedule must log again")
	}
}
