// Package daemon runs one monitoring instance.
//
// It owns the instance lock, the store, the observation loop and the control
// channel. It observes; it never acts on a watched session. There is
// deliberately no control method that can reach a worker — stopping birddog
// leaves every watched agent exactly as it was.
package daemon

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/BrutalSystems/birddog/internal/config"
	"github.com/BrutalSystems/birddog/internal/instance"
	"github.com/BrutalSystems/birddog/internal/ipc"
	"github.com/BrutalSystems/birddog/internal/machine"
	"github.com/BrutalSystems/birddog/internal/monitor"
	"github.com/BrutalSystems/birddog/internal/notify"
	"github.com/BrutalSystems/birddog/internal/platform/lockfile"
	"github.com/BrutalSystems/birddog/internal/store"
)

// Options configures one instance.
type Options struct {
	InstanceID string

	// Machine identifies the machine this instance observes from, empty in
	// local mode. Supplied, never minted; see internal/machine.
	Machine string

	Config      *config.Config
	ConfigPath  string
	SocketPath  string
	DBPath      string
	LockPath    string
	RegistryDir string
	Interval    time.Duration
	Observers   map[string]monitor.Observer

	// RetentionInterval is how often retention runs. Zero means
	// defaultRetentionInterval, the same way a zero Interval means
	// defaultInterval. Settable so a test can drive it without waiting
	// minutes.
	RetentionInterval time.Duration

	// Notifier delivers alerts to the orchestrator. Nil means None: alerts
	// stay in the event feed to be polled, which is always the fallback.
	Notifier notify.Notifier

	// Owner is the session this instance was started for. Nil means the
	// instance outlives everything and waits to be stopped explicitly, which
	// is the default.
	Owner *Owner

	// Now is injectable so tests can drive time exactly.
	Now func() time.Time
}

// defaultInterval is how often targets are observed. Frequent enough to notice
// a change promptly, cheap enough that watching a dozen sessions costs little.
const defaultInterval = 2 * time.Second

// defaultRetentionInterval is how often retention runs.
//
// Far slower than observation: it enforces a bound measured in hours or days,
// and a pass that finds nothing to do still costs two queries. Nothing about
// correctness depends on the value — a late pass keeps history slightly longer
// than asked, which is the safe direction.
const defaultRetentionInterval = 5 * time.Minute

// Daemon is a running instance.
type Daemon struct {
	opts    Options
	store   *store.Store
	lock    *lockfile.Lock
	server  *ipc.Server
	monitor *monitor.Monitor
	now     func() time.Time

	// ownerMisses counts consecutive checks in which the owner was absent.
	// Touched only from the observation loop.
	ownerMisses int

	// cfgMu guards the mutable configuration — the watch list and the retention
	// window — which commands may change while the observation and retention
	// loops are reading it.
	cfgMu     sync.Mutex
	configRev int64

	stop chan struct{}

	// done closes when the observation loop ends; released closes once
	// shutdown has finished giving everything back. Wait blocks on the
	// second, because a process that exits between them strands its socket,
	// its lock and its registry record.
	done     chan struct{}
	released chan struct{}
	stopOnce sync.Once

	// observed is closed and replaced after each pass, so a long poll can
	// wait on it rather than spinning.
	mu       sync.Mutex
	observed chan struct{}
	paused   bool
}

// Start brings an instance up: it takes the lock, opens the store, begins
// observing, and only then starts answering on the control channel.
func Start(opts Options) (*Daemon, error) {
	if opts.Interval <= 0 {
		opts.Interval = defaultInterval
	}
	if opts.RetentionInterval <= 0 {
		opts.RetentionInterval = defaultRetentionInterval
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}

	// The lock comes first: two daemons for one instance would write the same
	// database and duplicate every alert.
	lock, err := lockfile.Acquire(opts.LockPath)
	if err != nil {
		if lockfile.IsHeld(err) {
			return nil, fmt.Errorf("instance %s is already running: %w", opts.InstanceID, err)
		}
		return nil, err
	}

	st, err := store.Open(opts.DBPath)
	if err != nil {
		_ = lock.Release()
		return nil, err
	}

	mon := monitor.New(opts.InstanceID, opts.Config, st, opts.Observers)
	mon.SetMachine(opts.Machine)

	d := &Daemon{
		opts:     opts,
		store:    st,
		lock:     lock,
		monitor:  mon,
		now:      opts.Now,
		stop:     make(chan struct{}),
		done:     make(chan struct{}),
		released: make(chan struct{}),
		observed: make(chan struct{}),
	}

	if opts.Notifier != nil {
		d.monitor.SetNotifier(opts.Notifier)
	}

	server, err := ipc.Listen(opts.SocketPath, d.handle)
	if err != nil {
		_ = st.Close()
		_ = lock.Release()
		return nil, err
	}
	d.server = server

	if err := instance.Register(opts.RegistryDir, instance.Record{
		ID:         opts.InstanceID,
		Name:       opts.Config.Name,
		PID:        os.Getpid(),
		SocketPath: opts.SocketPath,
		ConfigPath: opts.ConfigPath,
		DBPath:     opts.DBPath,
		Owner:      ownerDescription(opts.Owner),
		StartedAt:  d.now().UTC(),
	}); err != nil {
		_ = server.Close()
		_ = st.Close()
		_ = lock.Release()
		return nil, err
	}

	go d.loop()
	return d, nil
}

// ownerDescription names the owner for the instance record, so resume can
// restore the tie rather than silently dropping it.
func ownerDescription(o *Owner) string {
	if o == nil {
		return ""
	}
	return o.Description
}

// loop observes until the instance is stopped. It runs on its own, so
// monitoring does not depend on anyone asking for it.
func (d *Daemon) loop() {
	defer close(d.done)

	ticker := time.NewTicker(d.opts.Interval)
	defer ticker.Stop()

	retention := time.NewTicker(d.opts.RetentionInterval)
	defer retention.Stop()

	d.pass()

	// Retention runs once here as well as on the ticker. Without it the first
	// enforcement is a whole interval away, so an instance shorter-lived than
	// that never applies a window it was configured with, and an operator who
	// sets one on an old store watches nothing happen for five minutes with no
	// way to tell that from a fault.
	//
	// Running it on every start is safe rather than merely tolerable: the floor
	// only ever rises, because PruneBefore refuses to lower it, so a frequently
	// restarted instance repeats the work and cannot undo any of it.
	d.retain()

	for {
		select {
		case <-d.stop:
			return
		case <-ticker.C:
			d.pass()
		case <-retention.C:
			d.retain()
		}
	}
}

// retain runs one retention pass, swallowing its error.
//
// A failing retention pass is not fatal, for the same reason a failing
// observation pass is not: the next one may succeed, and the monitoring that
// still works must not stop.
func (d *Daemon) retain() { _ = d.retentionPass(d.now()) }

func (d *Daemon) pass() {
	d.mu.Lock()
	paused := d.paused
	d.mu.Unlock()
	if paused {
		return
	}

	// A failing pass is not fatal. The next one may succeed, and stopping
	// would lose the monitoring that still works.
	d.cfgMu.Lock()
	err := d.monitor.Tick(d.now())
	d.cfgMu.Unlock()
	_ = err
	d.notifyObservers()

	// Checked after the pass, so the last thing the owner asked to be watched
	// is recorded before the instance goes.
	if d.ownerGone() {
		// From inside the loop, so shutdown cannot wait on the loop it is in.
		go func() { _ = d.Shutdown() }()
	}
}

// notifyObservers wakes anything waiting on new events.
func (d *Daemon) notifyObservers() {
	d.mu.Lock()
	defer d.mu.Unlock()
	close(d.observed)
	d.observed = make(chan struct{})
}

func (d *Daemon) observedChan() chan struct{} {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.observed
}

// PauseObservation stops further passes. It affects only birddog: every
// watched session carries on untouched.
func (d *Daemon) PauseObservation() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.paused = true
}

// PruneBefore drops retained events below a cursor.
func (d *Daemon) PruneBefore(seq int64) error { return d.store.PruneBefore(seq) }

// retentionPass drops history that is past the configured window, never
// crossing a terminal outcome nobody has collected.
//
// It does nothing at all when no window is configured, which is the default
// and is what birddog did before retention existed.
func (d *Daemon) retentionPass(now time.Time) error {
	d.cfgMu.Lock()
	retention := d.opts.Config.Retention
	d.cfgMu.Unlock()
	if retention == nil {
		return nil
	}

	from, err := d.store.RetainFrom(now.Add(-retention.MaxAge), retention.HoldUncollected)
	if err != nil {
		return err
	}
	if from <= 1 {
		// Nothing is old enough to drop, and PruneBefore(1) is not harmless:
		// it writes seq-1, so it would record a floor of zero, which
		// retentionFloor cannot tell apart from never having pruned. On a store
		// a real prune has already emptied that *resets* the floor and un-stales
		// cursors that must still be refused. PruneBefore now refuses to lower
		// the floor itself; this skips the pointless write as well.
		return nil
	}
	return d.store.PruneBefore(from)
}

// Done reports whether the observation loop has finished.
func (d *Daemon) Done() bool {
	select {
	case <-d.done:
		return true
	default:
		return false
	}
}

// Wait blocks until the instance has stopped and released everything it took.
func (d *Daemon) Wait() { <-d.released }

// Shutdown stops the instance and releases everything it took.
//
// It stops birddog only. Watched agents keep running; birddog holds nothing of
// theirs to release.
func (d *Daemon) Shutdown() error {
	var err error
	d.stopOnce.Do(func() {
		defer close(d.released)
		close(d.stop)
		<-d.done

		if d.server != nil {
			err = d.server.Close()
		}
		// The record is kept, marked stopped: it is what makes a stopped
		// instance discoverable and resumable, and its state on disk
		// outlives the process.
		if derr := instance.MarkStopped(d.opts.RegistryDir, d.opts.InstanceID, d.now().UTC()); err == nil {
			err = derr
		}
		if cerr := d.store.Close(); err == nil {
			err = cerr
		}
		if lerr := d.lock.Release(); err == nil {
			err = lerr
		}
	})
	return err
}

// handle serves one control request.
//
// The method set is the product boundary made checkable: there is no method
// here that sends to, approves for, or controls a watched worker.
func (d *Daemon) handle(method string, params json.RawMessage) (any, error) {
	switch method {
	case "status":
		return d.status()
	case "events":
		return d.events(params)
	case "ack":
		return d.ack(params)
	case "watch.add":
		return d.watchAdd(params)
	case "watch.remove":
		return d.watchRemove(params)
	case "watch.update":
		return d.watchUpdate(params)
	case "stop":
		go func() {
			// Reply first, then stop: the caller should hear that its request
			// was accepted rather than see the connection vanish.
			time.Sleep(20 * time.Millisecond)
			_ = d.Shutdown()
		}()
		return StopResult{Stopping: true, WatchedSessionsUnaffected: true}, nil
	default:
		return nil, &ipc.Error{Code: ipc.CodeUnknownMethod, Message: fmt.Sprintf("no method %q", method)}
	}
}

func (d *Daemon) status() (StatusResult, error) {
	cursor, err := d.store.LatestCursor()
	if err != nil {
		return StatusResult{}, err
	}

	result := StatusResult{
		Store:      d.store.ID(),
		Machine:    d.opts.Machine,
		InstanceID: d.opts.InstanceID,
		Name:       d.opts.Config.Name,
		PID:        os.Getpid(),
		Cursor:     cursor,
		ObservedAt: d.now().UTC(),
	}
	if d.opts.Owner != nil {
		result.Owner = d.opts.Owner.Description
	}

	d.cfgMu.Lock()
	watched := append([]config.Target(nil), d.opts.Config.Targets...)
	revision := d.configRev
	d.cfgMu.Unlock()
	result.ConfigRevision = revision

	for _, target := range watched {
		ts := TargetStatus{
			// Not set from d.opts here: it comes from the stored state
			// below, which knows where the session was actually observed.
			// A target with nothing stored yet has been observed nowhere and
			// carries no machine at all.
			ID:       target.ID,
			Provider: target.Provider,
			Labels:   target.Labels,
			// Until something is observed, birddog has not looked at all.
			// The observation below replaces this where a provider can see.
			InputRequestVisibility: "unavailable",
			ExpectedQuietReason:    target.Policy.ExpectedQuietReason,
			ExpectedQuietUntil:     target.Policy.ExpectedQuietUntil,
		}

		if state, ok, err := d.store.TargetState(target.ID); err != nil {
			return StatusResult{}, err
		} else if ok {
			ts.Status = state.Status
			ts.Generation = state.Generation
			ts.SessionID = state.SessionID
			ts.ObservedAt = &state.ObservedAt
			ts.Source = state.Source
			if state.InputRequestVisibility != "" {
				ts.InputRequestVisibility = state.InputRequestVisibility
			}
			ts.Reason = state.Reason
			// The machine the session was observed on, which an instance
			// resumed under a different identity must not overwrite with its
			// own. The stored reference is the only thing that knows.
			ts.Machine = state.Machine
			// A state is current only while it is still being observed. Any
			// open observation-loss incident means it is history.
			ts.StatusIsCurrent = true
		}

		incidents, err := d.store.OpenIncidents(target.ID)
		if err != nil {
			return StatusResult{}, err
		}
		for _, inc := range incidents {
			if inc.Condition == config.ConditionObservationLost {
				ts.StatusIsCurrent = false
			}
			ts.Incidents = append(ts.Incidents, IncidentView{
				ID:             inc.ID,
				Condition:      inc.Condition,
				Generation:     inc.Generation,
				OpenedAt:       inc.OpenedAt,
				LastSeenAt:     inc.LastSeenAt,
				NotifiedAt:     inc.NotifiedAt,
				AcknowledgedAt: inc.AcknowledgedAt,
			})
		}
		result.Targets = append(result.Targets, ts)
	}
	return result, nil
}

func (d *Daemon) events(params json.RawMessage) (EventsResult, error) {
	var p EventsParams
	if len(params) > 0 {
		if err := json.Unmarshal(params, &p); err != nil {
			return EventsResult{}, &ipc.Error{Code: ipc.CodeBadRequest, Message: err.Error()}
		}
	}
	if p.Limit <= 0 || p.Limit > 1000 {
		p.Limit = 100
	}
	// Machine first, then store: the coarser qualifier before the finer one.
	// Store ids are random per store, so a cursor from another machine always
	// carries a foreign store id too. Checking the store first made
	// machine_mismatch unreachable for a consumer following the documented
	// advice to send both, and answered it store_replaced instead — whose
	// remedy is to discard the position and re-baseline. Being pointed at the
	// wrong host would have cost it a position that was never lost.
	if err := machine.Check(p.Machine, d.opts.Machine); err != nil {
		return EventsResult{}, &ipc.Error{Code: ipc.CodeMachineMismatch, Message: err.Error()}
	}
	// Before the cursor is used for anything. A cursor from a replaced store
	// would otherwise pass the staleness check and match no rows, and the
	// empty page that results reads as "nothing has happened".
	if err := d.store.CheckIdentity(p.Store); err != nil {
		return EventsResult{}, &ipc.Error{Code: ipc.CodeStoreReplaced, Message: err.Error()}
	}

	wait, clamped := clampWait(p.WaitSeconds)
	p.WaitSeconds = wait

	deadline := time.Now().Add(time.Duration(p.WaitSeconds) * time.Second)
	for {
		events, err := d.store.After(p.After, p.Limit)
		if err != nil {
			if errors.Is(err, store.ErrCursorStale) {
				return EventsResult{}, &ipc.Error{Code: ipc.CodeCursorStale, Message: err.Error()}
			}
			return EventsResult{}, err
		}
		if len(events) > 0 {
			r := eventsResult(events)
			r.Store = d.store.ID()
			r.Machine = d.opts.Machine
			r.WaitSeconds, r.WaitClamped = wait, clamped
			return r, nil
		}
		if p.WaitSeconds <= 0 || !time.Now().Before(deadline) {
			// Caught up. An empty answer is a result, not a failure.
			return EventsResult{
				Events: []EventView{}, Cursor: p.After, Store: d.store.ID(),
				Machine: d.opts.Machine, WaitSeconds: wait, WaitClamped: clamped,
			}, nil
		}

		select {
		case <-d.observedChan():
		case <-time.After(time.Until(deadline)):
		case <-d.stop:
			return EventsResult{
				Events: []EventView{}, Cursor: p.After, Store: d.store.ID(),
				Machine: d.opts.Machine, WaitSeconds: wait, WaitClamped: clamped,
			}, nil
		}
	}
}

func eventsResult(events []store.Event) EventsResult {
	out := EventsResult{Events: make([]EventView, 0, len(events))}
	for _, e := range events {
		out.Events = append(out.Events, EventView{
			Cursor:     e.Seq,
			InstanceID: e.InstanceID,
			TargetID:   e.TargetID,
			Type:       e.Type,
			Source:     e.Source,
			At:         e.At,
			SessionID:  e.SessionID,
			Machine:    e.Machine,
			Generation: e.Generation,
			Status:     e.Status,
			Reason:     e.Reason,
			Evidence:   e.Evidence,
		})
		out.Cursor = e.Seq
	}
	return out
}

func (d *Daemon) ack(params json.RawMessage) (any, error) {
	var p AckParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &ipc.Error{Code: ipc.CodeBadRequest, Message: err.Error()}
	}
	if p.IncidentID == 0 {
		return nil, &ipc.Error{Code: ipc.CodeBadRequest, Message: "incident_id is required"}
	}
	if prior, ok, err := d.priorResult(p.IdempotencyKey); err != nil || ok {
		return prior, err
	}

	if err := d.store.AcknowledgeIncident(p.IncidentID, d.now().UTC()); err != nil {
		return nil, &ipc.Error{Code: ipc.CodeNotFound, Message: err.Error()}
	}
	// Acknowledgement records that the orchestrator has seen the alert. It
	// resolves no request and changes nothing about the worker.
	return d.remember(p.IdempotencyKey, AckResult{
		IncidentID:   p.IncidentID,
		Acknowledged: true,
		Note:         "recorded as seen; the observed condition is unchanged",
	})
}
