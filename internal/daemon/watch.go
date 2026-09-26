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
	return current, nil
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
