package daemon

import "testing"

func TestAWaitWithinTheBoundIsHonoured(t *testing.T) {
	for _, requested := range []int{1, 10, MaxWaitSeconds} {
		applied, clamped := clampWait(requested)
		if applied != requested || clamped {
			t.Errorf("clampWait(%d) = %d clamped=%v, want it honoured", requested, applied, clamped)
		}
	}
}

// An hour-long poll was accepted before. Nothing refused it.
func TestAWaitBeyondTheBoundIsClamped(t *testing.T) {
	applied, clamped := clampWait(3600)
	if applied != MaxWaitSeconds || !clamped {
		t.Errorf("clampWait(3600) = %d clamped=%v, want it bounded to %d", applied, clamped, MaxWaitSeconds)
	}
}

// Returning early while saying nothing is indistinguishable from having waited
// the full time and seen no events. The caller must be able to tell.
func TestAClampedWaitIsReportedRatherThanAppliedSilently(t *testing.T) {
	if _, clamped := clampWait(MaxWaitSeconds + 1); !clamped {
		t.Error("a wait one second over the bound was clamped without saying so")
	}
}

func TestNoWaitIsNotAClamp(t *testing.T) {
	applied, clamped := clampWait(0)
	if applied != 0 || clamped {
		t.Errorf("clampWait(0) = %d clamped=%v, want an immediate return and no clamp", applied, clamped)
	}
}

// The bound exists to sit below the transport's idle timeout; a bound at or
// above it would not do its job.
func TestTheBoundSitsBelowTheTransportIdleTimeout(t *testing.T) {
	const transportIdleTimeoutSeconds = 30
	if MaxWaitSeconds >= transportIdleTimeoutSeconds {
		t.Errorf("MaxWaitSeconds = %d, want it clearly below %d", MaxWaitSeconds, transportIdleTimeoutSeconds)
	}
}
