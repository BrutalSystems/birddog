package wake

import (
	"testing"
	"time"
)

var t0 = time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)

// Normal operation: both clocks advance together.
func TestNoJumpWhenBothClocksAgree(t *testing.T) {
	d := New(time.Minute)
	d.Mark(t0, 0)

	if got := d.Check(t0.Add(2*time.Second), 2*time.Second); got.Jumped {
		t.Errorf("Jumped = true for an ordinary tick: %+v", got)
	}
}

// macOS does not advance the monotonic clock while asleep, so a sleep shows up
// as wall-clock time passing that monotonic time did not.
func TestSleepShowsAsWallClockOutrunningMonotonic(t *testing.T) {
	d := New(time.Minute)
	d.Mark(t0, 0)

	// Two hours of wall clock, two seconds of monotonic: the machine slept.
	got := d.Check(t0.Add(2*time.Hour), 2*time.Second)
	if !got.Jumped {
		t.Fatal("Jumped = false after a two-hour sleep")
	}
	if got.Skipped < 90*time.Minute {
		t.Errorf("Skipped = %v, want roughly the two hours that passed", got.Skipped)
	}
}

// A small divergence is scheduling noise, not a sleep. Treating it as one
// would reset thresholds constantly and mean quiet never fires.
func TestSmallDivergenceIsNotAJump(t *testing.T) {
	d := New(time.Minute)
	d.Mark(t0, 0)

	if got := d.Check(t0.Add(3*time.Second), 2*time.Second); got.Jumped {
		t.Errorf("Jumped = true for a one-second divergence: %+v", got)
	}
}

// A clock set backwards must not be read as time having passed.
func TestBackwardsClockIsAJumpButNotElapsedTime(t *testing.T) {
	d := New(time.Minute)
	d.Mark(t0, 0)

	got := d.Check(t0.Add(-time.Hour), 2*time.Second)
	if !got.Jumped {
		t.Error("Jumped = false when the wall clock went backwards")
	}
	if got.Skipped < 0 {
		t.Errorf("Skipped = %v, want a non-negative duration", got.Skipped)
	}
}

// The first check has nothing to compare against, so it cannot be a jump.
func TestFirstCheckIsNeverAJump(t *testing.T) {
	d := New(time.Minute)
	if got := d.Check(t0, 0); got.Jumped {
		t.Error("the first check reported a jump")
	}
}

// After a jump, thresholds restart from the wake rather than from activity
// that predates the sleep — otherwise every target looks quiet on wake.
func TestJumpProducesAThresholdFloorAtTheWake(t *testing.T) {
	d := New(time.Minute)
	d.Mark(t0, 0)

	now := t0.Add(2 * time.Hour)
	got := d.Check(now, 2*time.Second)
	if !got.Floor.Equal(now) {
		t.Errorf("Floor = %v, want the moment of waking %v", got.Floor, now)
	}
}

func TestCheckUpdatesTheBaseline(t *testing.T) {
	d := New(time.Minute)
	d.Mark(t0, 0)

	_ = d.Check(t0.Add(2*time.Hour), 2*time.Second)
	// The next ordinary tick, measured from the new baseline, is not a jump.
	if got := d.Check(t0.Add(2*time.Hour).Add(2*time.Second), 4*time.Second); got.Jumped {
		t.Errorf("Jumped = true for an ordinary tick after a wake: %+v", got)
	}
}
