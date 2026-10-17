package daemon

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/BrutalSystems/birddog/internal/config"
	"github.com/BrutalSystems/birddog/internal/ipc"
)

// Watch mutations change what an instance observes, without restarting it or
// disturbing anything it is already watching.
//
// Every mutation is validated in full before any of it is applied, and
// advances a revision — so a delayed observation can be told apart from one
// made under the current rules. Changing thresholds never rewrites history:
// the event feed is what was observed, not what the rules were.

// WatchAddParams registers a new target.
type WatchAddParams struct {
	// Spec is the target in the configuration file's format, and is what
	// `watch add` sends. Parsed here exactly as a target in a file is, with
	// the same defaults and validation.
	Spec json.RawMessage `json:"spec,omitempty"`

	// Target is the target as an in-memory structure, which is what earlier
	// clients send. Its thresholds cannot cross the wire in that form, so
	// what was left unset is given the file's defaults rather than zero.
	Target config.Target `json:"target"`

	// IdempotencyKey lets a caller retry a request it did not hear the answer
	// to. Repeating a key returns the original result instead of applying the
	// change again.
	IdempotencyKey string `json:"idempotency_key,omitempty"`
}

// WatchRemoveParams stops observing a target.
//
// Removing a target stops birddog watching it. It does nothing to the session.
type WatchRemoveParams struct {
	TargetID       string `json:"target_id"`
	IdempotencyKey string `json:"idempotency_key,omitempty"`
}

// WatchUpdateParams adjusts one target's policy. Every field is optional; only
// those supplied are changed.
type WatchUpdateParams struct {
	TargetID string `json:"target_id"`

	AlertOn           []string `json:"alert_on,omitempty"`
	QuietAfterSeconds *int     `json:"quiet_after_seconds,omitempty"`
	IdleGraceSeconds  *int     `json:"idle_grace_seconds,omitempty"`

	// ExpectedQuietUntil is an RFC3339 time, or an empty string to clear the
	// override when the wait ends early.
	ExpectedQuietUntil  *string `json:"expected_quiet_until,omitempty"`
	ExpectedQuietReason string  `json:"expected_quiet_reason,omitempty"`

	// Deadline is an RFC3339 time by which the target is expected to have
	// finished, or an empty string to clear it. Moving it resolves an open
	// overdue incident if the new time is ahead; the next pass decides.
	Deadline *string `json:"deadline,omitempty"`

	IdempotencyKey string `json:"idempotency_key,omitempty"`
}

// WatchResult confirms a change, and says plainly what it did not do.
type WatchResult struct {
	ConfigRevision int64  `json:"config_revision"`
	Targets        int    `json:"targets"`
	Note           string `json:"note"`
}

const watchNote = "monitoring only; the watched session was not touched"

func (d *Daemon) watchAdd(params json.RawMessage) (any, error) {
	var p WatchAddParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &ipc.Error{Code: ipc.CodeBadRequest, Message: err.Error()}
	}
	if prior, ok, err := d.priorResult(p.IdempotencyKey); err != nil || ok {
		return prior, err
	}
	if len(p.Spec) > 0 {
		target, err := config.ParseTarget(p.Spec)
		if err != nil {
			return nil, &ipc.Error{Code: ipc.CodeBadRequest, Message: err.Error()}
		}
		p.Target = target
	} else {
		p.Target.Policy = config.FillDefaults(p.Target.Policy)
	}

	d.cfgMu.Lock()
	defer d.cfgMu.Unlock()

	for _, t := range d.opts.Config.Targets {
		if t.ID == p.Target.ID {
			return nil, &ipc.Error{
				Code:    ipc.CodeBadRequest,
				Message: fmt.Sprintf("target %q is already watched by this instance", p.Target.ID),
			}
		}
	}
	if err := config.ValidateTarget(p.Target); err != nil {
		return nil, &ipc.Error{Code: ipc.CodeBadRequest, Message: err.Error()}
	}

	now := d.now()
	p.Target.Policy = config.ResolveDeadline(p.Target.Policy, now)
	d.addedAt[p.Target.ID] = now

	// Nothing learned about an earlier target under this id carries over.
	d.monitor.Forget(p.Target.ID)
	d.opts.Config.Targets = append(d.opts.Config.Targets, p.Target)
	return d.remember(p.IdempotencyKey, d.bumpRevision())
}

func (d *Daemon) watchRemove(params json.RawMessage) (any, error) {
	var p WatchRemoveParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &ipc.Error{Code: ipc.CodeBadRequest, Message: err.Error()}
	}
	if prior, ok, err := d.priorResult(p.IdempotencyKey); err != nil || ok {
		return prior, err
	}

	d.cfgMu.Lock()
	defer d.cfgMu.Unlock()

	kept := make([]config.Target, 0, len(d.opts.Config.Targets))
	found := false
	for _, t := range d.opts.Config.Targets {
		if t.ID == p.TargetID {
			found = true
			continue
		}
		kept = append(kept, t)
	}
	if !found {
		return nil, &ipc.Error{
			Code:    ipc.CodeNotFound,
			Message: fmt.Sprintf("no target %q on this instance", p.TargetID),
		}
	}

	// The target's events and incidents are kept: they are what was observed,
	// and stopping watching does not make them untrue.
	d.opts.Config.Targets = kept
	delete(d.addedAt, p.TargetID)
	d.monitor.Forget(p.TargetID)
	return d.remember(p.IdempotencyKey, d.bumpRevision())
}

func (d *Daemon) watchUpdate(params json.RawMessage) (any, error) {
	var p WatchUpdateParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &ipc.Error{Code: ipc.CodeBadRequest, Message: err.Error()}
	}
	if prior, ok, err := d.priorResult(p.IdempotencyKey); err != nil || ok {
		return prior, err
	}

	d.cfgMu.Lock()
	defer d.cfgMu.Unlock()

	for i, t := range d.opts.Config.Targets {
		if t.ID != p.TargetID {
			continue
		}
		// Build the new policy beside the old one, so a rejected change
		// leaves the target exactly as it was.
		updated, err := applyPolicyUpdate(t.Policy, p)
		if err != nil {
			return nil, &ipc.Error{Code: ipc.CodeBadRequest, Message: err.Error()}
		}
		d.opts.Config.Targets[i].Policy = updated
		if err := d.resolveUnaskedOverdue(d.opts.Config.Targets[i]); err != nil {
			return nil, err
		}
		return d.remember(p.IdempotencyKey, d.bumpRevision())
	}

	return nil, &ipc.Error{
		Code:    ipc.CodeNotFound,
		Message: fmt.Sprintf("no target %q on this instance", p.TargetID),
	}
}

func applyPolicyUpdate(current config.Policy, p WatchUpdateParams) (config.Policy, error) {
	if p.AlertOn != nil {
		if err := config.ValidateConditions(p.AlertOn); err != nil {
			return config.Policy{}, err
		}
		current.AlertOn = append([]string(nil), p.AlertOn...)
	}
	if p.QuietAfterSeconds != nil {
		if *p.QuietAfterSeconds <= 0 {
			return config.Policy{}, fmt.Errorf("quiet_after_seconds must be positive")
		}
		current.QuietAfter = time.Duration(*p.QuietAfterSeconds) * time.Second
	}
	if p.IdleGraceSeconds != nil {
		if *p.IdleGraceSeconds < 0 {
			return config.Policy{}, fmt.Errorf("idle_grace_seconds must not be negative")
		}
		current.IdleGrace = time.Duration(*p.IdleGraceSeconds) * time.Second
	}
	if p.ExpectedQuietUntil != nil {
		if *p.ExpectedQuietUntil == "" {
			// Clearing the override: the wait ended early.
			current.ExpectedQuietUntil = nil
			current.ExpectedQuietReason = ""
		} else {
			until, err := time.Parse(time.RFC3339, *p.ExpectedQuietUntil)
			if err != nil {
				return config.Policy{}, fmt.Errorf("expected_quiet_until: %w", err)
			}
			current.ExpectedQuietUntil = &until
			current.ExpectedQuietReason = p.ExpectedQuietReason
		}
	}
	if p.Deadline != nil {
		// Either way the deadline in force is now this update's. What the
		// file said is kept beside it, so a sync of the same file can tell
		// that nothing there changed.
		current.DeadlineByUpdate = true
		if *p.Deadline == "" {
			current.Deadline = nil
		} else {
			at, err := time.Parse(time.RFC3339, *p.Deadline)
			if err != nil {
				return config.Policy{}, fmt.Errorf("deadline: %w", err)
			}
			current.Deadline = &at
		}
	}
	if err := config.ValidateDeadline(current); err != nil {
		return config.Policy{}, err
	}
	return current, nil
}

// resolveUnaskedOverdue resolves an open overdue incident on a target whose
// policy no longer asks about overdue. Callers hold cfgMu.
//
// The policy deliberately leaves a condition it is not asked about alone, so
// once alert_on drops overdue nothing would ever close one already open: not
// the exit, and not clearing the deadline. Overdue is the one condition whose
// incident outlives the setting that raised it, because the setting is a
// moment rather than an observation, so it is closed here, when the change is
// made.
func (d *Daemon) resolveUnaskedOverdue(t config.Target) error {
	if t.Policy.AlertsOn(config.ConditionOverdue) {
		return nil
	}
	open, err := d.store.OpenIncidents(t.ID)
	if err != nil {
		return err
	}
	for _, inc := range open {
		if inc.Condition == config.ConditionOverdue {
			if err := d.store.ResolveIncident(inc.ID, d.now()); err != nil {
				return err
			}
		}
	}
	return nil
}

// bumpRevision records that the watch list changed. Callers hold cfgMu.
func (d *Daemon) bumpRevision() WatchResult {
	d.configRev++
	d.monitor.SetConfigRevision(d.configRev)
	return WatchResult{
		ConfigRevision: d.configRev,
		Targets:        len(d.opts.Config.Targets),
		Note:           watchNote,
	}
}

// priorResult returns the answer a key's first use produced, if any.
func (d *Daemon) priorResult(key string) (any, bool, error) {
	if key == "" {
		return nil, false, nil
	}
	raw, ok, err := d.store.PriorResult(key)
	if err != nil || !ok {
		return nil, false, err
	}
	// Returned verbatim: a retry is entitled to exactly what the original
	// request produced.
	return json.RawMessage(raw), true, nil
}

// remember records a mutation's result against its key, so a retry can be
// answered without applying the change again.
//
// Every mutation that accepts a key must call this. Checking priorResult
// without recording one looks correct and is not: the key is never stored, so
// every retry re-applies the change. A deterministic mutation hides it, because
// the reply is identical either way and only the durable state moves.
func (d *Daemon) remember(key string, result any) (any, error) {
	if key == "" {
		return result, nil
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		return nil, err
	}
	if err := d.store.RememberResult(key, encoded); err != nil {
		return nil, err
	}
	return result, nil
}
