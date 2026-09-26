// Package wake notices when the machine has been asleep or the clock moved.
//
// macOS does not advance the monotonic clock while asleep, so a sleep appears
// as wall-clock time passing that monotonic time did not. Without noticing, a
// laptop closed overnight wakes to find every target silent for eight hours
// and reports a storm of alerts about time nobody was watching.
package wake

import "time"

// Detector compares the two clocks between observation passes.
type Detector struct {
	// threshold is how far the clocks may diverge before it counts. Small
	// divergences are scheduling noise; treating them as sleeps would reset
	// thresholds constantly and mean quiet never fires at all.
	threshold time.Duration

	have     bool
	lastWall time.Time
	lastMono time.Duration
}

// Result is what one comparison found.
type Result struct {
	// Jumped is true when the clocks disagreed by more than the threshold.
	Jumped bool

	// Skipped is roughly how much wall-clock time passed unobserved.
	Skipped time.Duration

	// Floor is the moment to measure thresholds from after a jump, so quiet
	// and idle restart at the wake rather than firing immediately about a
	// silence nobody was there to see.
	Floor time.Time
}

// New builds a detector. A threshold of a minute is comfortably above
// scheduling noise and well below any sleep worth noticing.
func New(threshold time.Duration) *Detector {
	return &Detector{threshold: threshold}
}

// Mark sets the baseline without reporting anything.
func (d *Detector) Mark(wall time.Time, mono time.Duration) {
	d.have = true
	d.lastWall = wall
	d.lastMono = mono
}

// Check compares this pass against the previous one and updates the baseline.
//
// mono is a monotonic elapsed reading — time since the process started, not a
// wall-clock time. The two are compared, never mixed: live timers run on
// monotonic elapsed time and the durable record keeps wall-clock stamps.
func (d *Detector) Check(wall time.Time, mono time.Duration) Result {
	if !d.have {
		d.Mark(wall, mono)
		return Result{}
	}

	wallDelta := wall.Sub(d.lastWall)
	monoDelta := mono - d.lastMono
	d.Mark(wall, mono)

	divergence := wallDelta - monoDelta
	if divergence < 0 {
		divergence = -divergence
	}
	if divergence <= d.threshold {
		return Result{}
	}

	skipped := wallDelta - monoDelta
	if skipped < 0 {
		// The clock was set backwards. Time did not pass; the reading moved.
		skipped = 0
	}
	return Result{Jumped: true, Skipped: skipped, Floor: wall}
}
