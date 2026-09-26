package daemon

// MaxWaitSeconds bounds how long a long poll may be held open.
//
// Two reasons, and the second is why the number is what it is.
//
// An unbounded wait is a loose end on its own: the value comes from the caller
// and became a deadline with nothing refusing an hour.
//
// The bound itself is chosen to sit clearly below the idle timeout of the
// transport intended for cross-machine delivery, which closes a connection
// after thirty seconds without traffic. A poll that runs to exactly that
// boundary races it, and the loser is a caller who cannot tell a closed
// connection from an empty answer.
const MaxWaitSeconds = 25

// clampWait limits a requested wait to MaxWaitSeconds.
//
// The caller is told what it got. Returning early while reporting nothing is
// indistinguishable from having genuinely waited and seen no events, and those
// are different facts.
func clampWait(requested int) (applied int, clamped bool) {
	if requested <= 0 {
		return 0, false
	}
	if requested > MaxWaitSeconds {
		return MaxWaitSeconds, true
	}
	return requested, false
}
