package observe

import (
	"fmt"
	"sync"
	"time"

	"github.com/BrutalSystems/birddog/internal/config"
	"github.com/BrutalSystems/birddog/internal/monitor"
	"github.com/BrutalSystems/birddog/internal/platform/proc"
	"github.com/BrutalSystems/birddog/internal/policy"
	"github.com/BrutalSystems/birddog/internal/provider"
)

// Process observes a registered process and the files it was said to write.
//
// It is the fallback where no provider adapter can see inside a session: it
// knows the process is running, whether a child is doing work, and whether
// named files are growing. It does not know what the agent is thinking, and
// does not pretend to.
type Process struct {
	SameProcess func(pid int, procStart string) bool
	Descendants func(pid int) ([]int, error)

	// watchers persist between passes: a watcher's first poll is only a
	// baseline, so rebuilding one each time would see nothing ever change.
	mu       sync.Mutex
	watchers map[string]*Watcher
}

// Observe reports what can be established about a registered process.
func (p *Process) Observe(target config.Target) (monitor.Sighting, error) {
	pid, procStart := target.Attachment.PID, target.Attachment.ProcStart
	if pid == 0 {
		return unreadable(policy.ReasonSourceUnreadable), fmt.Errorf("target %q: a process target needs a pid", target.ID)
	}

	sameProcess := p.SameProcess
	if sameProcess == nil {
		sameProcess = proc.SameProcess
	}
	identity := fmt.Sprintf("pid:%d@%s", pid, procStart)

	if procStart != "" && !sameProcess(pid, procStart) {
		// Either nothing holds the pid, or something else does. The process
		// that was registered is gone either way, and the replacement is not
		// adopted in its place.
		return monitor.Sighting{
			Observation: policy.Observation{
				Live: false, Status: policy.StatusExited, StatusKnown: true,
				ActivityResolution: policy.ResolutionUnavailable,
			},
			SessionIdentity: identity,
		}, nil
	}

	descendants := p.Descendants
	if descendants == nil {
		descendants = proc.Descendants
	}
	children, childErr := descendants(pid)

	activity, watchErr := p.pollFiles(target)

	// A target that names files or logs is being watched at activity
	// resolution, and one that names none has nothing watching it at all.
	//
	// Deliberately not a check on the timestamp: a named path that does not
	// exist yet leaves LastChangeAt zero while still being watched, so deciding
	// from the value would flip the resolution the moment the file appeared,
	// when nothing about what is watching had changed.
	resolution := policy.ResolutionUnavailable
	if len(target.Observations.Files)+len(target.Observations.Logs) > 0 {
		resolution = policy.ResolutionActivity
	}

	obs := policy.Observation{
		Live:               true,
		Status:             policy.StatusUnknown,
		LastActivityAt:     activity.LastChangeAt,
		ActivityResolution: resolution,
		ObservedAt:         time.Now().UTC(),
		// A process tells you nothing about what its agent is waiting for.
		InputRequestVisibility: policy.VisibilityUnavailable,
	}

	// A child process is the work. Without this a session blocked on a long
	// compile is indistinguishable from one that has stopped — and its own
	// CPU being idle proves nothing.
	if len(children) > 0 {
		obs.Status = policy.StatusRunningTool
		obs.StatusKnown = true
		obs.StatusSince = obs.ObservedAt
	}

	sighting := monitor.Sighting{Observation: obs, SessionIdentity: identity}

	// A gap in coverage is reported. It is never allowed to read as silence.
	if watchErr != nil {
		return sighting, watchErr
	}
	if childErr != nil {
		return sighting, fmt.Errorf("list descendants of %d: %w", pid, childErr)
	}
	return sighting, nil
}

// pollFiles checks the paths this target named, keeping the watcher between
// passes so a change can be seen against the previous sighting.
func (p *Process) pollFiles(target config.Target) (FileActivity, error) {
	paths := append(append([]string{}, target.Observations.Files...), target.Observations.Logs...)
	if len(paths) == 0 {
		return FileActivity{}, nil
	}

	p.mu.Lock()
	if p.watchers == nil {
		p.watchers = map[string]*Watcher{}
	}
	w, ok := p.watchers[target.ID]
	if !ok {
		w = NewWatcher(paths)
		p.watchers[target.ID] = w
	}
	p.mu.Unlock()

	activity := w.Poll()
	if len(activity.Unavailable) > 0 {
		return activity, fmt.Errorf("target %q: cannot read %v", target.ID, activity.Unavailable)
	}
	return activity, nil
}

func init() {
	// The fallback where no adapter can see inside a session: process
	// identity, descendants, and explicitly named files.
	provider.Register(provider.Spec{Name: "process", Internal: true})
	register("process", func(Deps) (monitor.Observer, error) { return &Process{}, nil })
}
