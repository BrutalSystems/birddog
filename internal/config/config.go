// Package config reads an instance's watch list.
//
// Validation is strict and all-or-nothing. An unknown field is refused rather
// than ignored, because a silently dropped setting is a policy the operator
// wrote and never got — and a partially applied config leaves an instance
// watching something nobody asked for.
package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"github.com/BrutalSystems/birddog/internal/provider"
	"path/filepath"
	"strings"
	"time"
)

// SchemaVersion is the only configuration version this build understands.
const SchemaVersion = 1

// Attention conditions. These are the observations birddog can surface; the
// orchestrator decides what any of them mean.
const (
	ConditionIdle            = "idle"
	ConditionInputRequested  = "input_requested"
	ConditionExit            = "exit"
	ConditionQuiet           = "quiet"
	ConditionObservationLost = "observation_lost"

	// ConditionNoProgress: the session asserts it is working, and nothing has
	// been observed to happen for a long time.
	//
	// Separate from quiet on purpose. Quiet is suppressed while a provider
	// reports work, because a provider need not restamp a status that has not
	// changed — a legitimately busy session carries an old timestamp, and
	// reading that as silence would report a stall nobody observed. That
	// reasoning is right and stays.
	//
	// Its cost is that a session wedged while reporting work raises nothing
	// at all. This covers that, at a horizon far above the quiet threshold so
	// the reasoning above still holds.
	ConditionNoProgress = "no_progress"
)

// Default thresholds, from the handoff. Both are heuristics, not findings.
const (
	DefaultQuietAfter = 5 * time.Minute
	DefaultIdleGrace  = 30 * time.Second
)

// defaultAlerts deliberately omits idle. On Claude Code a Stop event ends every
// turn, so a session waiting on its human looks idle within the grace period
// and would alert once per turn, per target. Idle is opt-in.
var defaultAlerts = []string{
	ConditionInputRequested,
	ConditionExit,
	ConditionQuiet,
	ConditionObservationLost,
}

var knownConditions = map[string]bool{
	ConditionIdle:            true,
	ConditionNoProgress:      true,
	ConditionInputRequested:  true,
	ConditionExit:            true,
	ConditionQuiet:           true,
	ConditionObservationLost: true,
}

// Config is one instance's watch list.
type Config struct {
	SchemaVersion int           `json:"schema_version"`
	Name          string        `json:"name"`
	Notification  *Notification `json:"notification,omitempty"`
	Retention     *Retention    `json:"retention,omitempty"`
	Targets       []Target      `json:"targets"`
}

// Notification is where this instance's alerts are delivered.
//
// The recipient is the orchestrator, never a watched worker. Options are
// connector-specific; each connector documents its own keys and refuses ones
// it does not understand.
type Notification struct {
	Kind    string            `json:"kind"`
	Options map[string]string `json:"options,omitempty"`
}

// Retention bounds how much history an instance keeps.
//
// Nil means keep everything, which is what birddog did before this existed and
// is still the default. There is deliberately no default window: one that
// deletes a consumer's history is a number nobody has justified, which is the
// reasoning docs/decisions.md D6 records for no_progress.
type Retention struct {
	// MaxAge is how far back the event log is kept.
	MaxAge time.Duration

	// HoldUncollected is how much longer a terminal outcome nobody has
	// acknowledged survives past MaxAge, measured from the moment MaxAge would
	// have dropped it.
	//
	// Required whenever MaxAge is set. A terminal outcome is the one thing a
	// consumer cannot re-observe, so it is held; holding it forever for a
	// consumer that will never return is a leak. The bound is the part an
	// operator states rather than inherits.
	HoldUncollected time.Duration
}

// maxRetentionSeconds bounds both retention settings at ten years.
//
// This is not an opinion about how long history is worth keeping. It guards
// the seconds-to-Duration multiplication below: a large enough seconds value
// wraps time.Duration, and a hold that has wrapped negative empties the held
// set and prunes the terminal outcome the hold exists to protect. The failure
// would be silent and in the dangerous direction, so the input is bounded
// where it arrives rather than defended against where it is used.
const maxRetentionSeconds = 10 * 365 * 24 * 60 * 60

// retentionWire mirrors Retention with durations as seconds, which is what the
// file format uses.
type retentionWire struct {
	MaxAgeSeconds          *int `json:"max_age_seconds,omitempty"`
	HoldUncollectedSeconds *int `json:"hold_uncollected_seconds,omitempty"`
}

// Target is one watched session.
//
// There is deliberately no endpoint reference: opencode exposes no endpoint,
// and Codex exposes none an external observer can reach, so a field for one
// would describe a route that does not exist.
type Target struct {
	ID         string            `json:"id"`
	Provider   string            `json:"provider"`
	Attachment Attachment        `json:"attachment"`
	Workspace  string            `json:"workspace,omitempty"`
	Labels     map[string]string `json:"labels,omitempty"`
	Policy     Policy            `json:"policy"`

	// Observations are explicitly named files and logs. Nothing is watched
	// implicitly: there is no recursive or whole-home scanning.
	Observations Observations `json:"observations,omitempty"`
}

// Observations names the files and logs a target's work touches.
type Observations struct {
	Files []string `json:"files,omitempty"`
	Logs  []string `json:"logs,omitempty"`

	// Transcript opts this target into reading the session's conversation
	// transcript, which is what distinguishes a session blocked on a
	// question from one whose turn simply ended. Off by default: it is real
	// work on every pass, and one of its two signals is a heuristic an
	// operator should choose rather than inherit.
	Transcript bool `json:"transcript,omitempty"`
}

// Attachment identifies the session to observe.
type Attachment struct {
	Kind      string `json:"kind"`
	SessionID string `json:"session_id,omitempty"`
	PID       int    `json:"pid,omitempty"`
	ProcStart string `json:"proc_start,omitempty"`
}

// Policy is what this target's operator wants to hear about.
type Policy struct {
	AlertOn    []string      `json:"alert_on,omitempty"`
	QuietAfter time.Duration `json:"-"`
	IdleGrace  time.Duration `json:"-"`

	// NoProgressAfter is how long a session may assert work with nothing
	// observed to happen before no_progress opens.
	//
	// Zero disables it, and there is deliberately no default. The threshold
	// is the whole design: too short reintroduces the false stalls quiet's
	// suppression exists to prevent, and too long never fires. What is
	// appropriate depends on the work being watched, so an operator names it
	// or the condition stays off.
	//
	// It also depends on what the provider's activity timestamp tracks, which
	// is not the same thing everywhere. An instrumented session restamps it at
	// each tool boundary, so the duration measures silence. Uninstrumented
	// Claude Code restamps only on a transition, so it measures the age of the
	// turn: across 99 observed working runs the timestamp never moved within
	// the run in 98 of them. A single default would therefore mean two
	// different things on two providers, which is why there is none rather
	// than one nobody could read. See docs/decisions.md D6.
	NoProgressAfter time.Duration `json:"-"`

	// ExpectedQuietUntil suppresses quiet and idle alerts for a while. It
	// never suppresses exit, input-request or observation-loss alerts, and it
	// changes nothing about what is observed or recorded.
	ExpectedQuietUntil  *time.Time `json:"expected_quiet_until,omitempty"`
	ExpectedQuietReason string     `json:"expected_quiet_reason,omitempty"`
}

// AlertsOn reports whether this target should surface a condition.
func (p Policy) AlertsOn(condition string) bool {
	for _, c := range p.AlertOn {
		if c == condition {
			return true
		}
	}
	return false
}

// policyWire mirrors Policy with durations as seconds, which is what the file
// format uses and what an orchestrator writing one can express.
type policyWire struct {
	AlertOn             []string `json:"alert_on,omitempty"`
	QuietAfterSeconds   *int     `json:"quiet_after_seconds,omitempty"`
	IdleGraceSeconds    *int     `json:"idle_grace_seconds,omitempty"`
	NoProgressAfterSecs *int     `json:"no_progress_after_seconds,omitempty"`
	ExpectedQuietUntil  *string  `json:"expected_quiet_until,omitempty"`
	ExpectedQuietReason string   `json:"expected_quiet_reason,omitempty"`
}

type targetWire struct {
	ID         string            `json:"id"`
	Provider   string            `json:"provider"`
	Attachment Attachment        `json:"attachment"`
	Workspace  string            `json:"workspace,omitempty"`
	Labels     map[string]string `json:"labels,omitempty"`
	Policy     *policyWire       `json:"policy,omitempty"`

	Observations *Observations `json:"observations,omitempty"`
}

type configWire struct {
	SchemaVersion *int           `json:"schema_version"`
	Name          string         `json:"name"`
	Notification  *Notification  `json:"notification,omitempty"`
	Retention     *retentionWire `json:"retention,omitempty"`
	Targets       []targetWire   `json:"targets"`
}

// Parse reads and validates a configuration.
//
// On any error it returns a nil config: nothing is applied unless all of it is
// valid.
func Parse(data []byte) (*Config, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()

	var wire configWire
	if err := dec.Decode(&wire); err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}

	if wire.SchemaVersion == nil {
		return nil, fmt.Errorf("config: schema_version is required")
	}
	if *wire.SchemaVersion != SchemaVersion {
		return nil, fmt.Errorf("config: schema_version %d is not supported (this build understands %d)",
			*wire.SchemaVersion, SchemaVersion)
	}
	if strings.TrimSpace(wire.Name) == "" {
		return nil, fmt.Errorf("config: name is required")
	}

	retention, err := buildRetention(wire.Retention)
	if err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}

	cfg := &Config{
		SchemaVersion: *wire.SchemaVersion,
		Name:          wire.Name,
		Notification:  wire.Notification,
		Retention:     retention,
	}

	seen := map[string]bool{}
	for i, tw := range wire.Targets {
		target, err := buildTarget(tw)
		if err != nil {
			return nil, fmt.Errorf("config: target %d: %w", i, err)
		}
		if seen[target.ID] {
			return nil, fmt.Errorf("config: duplicate target id %q", target.ID)
		}
		seen[target.ID] = true
		cfg.Targets = append(cfg.Targets, target)
	}
	return cfg, nil
}

// ValidateTarget checks a target built in memory, so a watch added at runtime
// is held to exactly the rules a configuration file is.
func ValidateTarget(t Target) error {
	if strings.TrimSpace(t.ID) == "" {
		return fmt.Errorf("id is required")
	}
	if !provider.Known(t.Provider) {
		return fmt.Errorf("unknown provider %q", t.Provider)
	}
	if t.Attachment.SessionID == "" && t.Attachment.PID == 0 {
		return fmt.Errorf("attachment needs a session_id or a pid")
	}
	if t.Workspace != "" && !filepath.IsAbs(t.Workspace) {
		return fmt.Errorf("workspace %q must be an absolute path", t.Workspace)
	}
	return ValidateConditions(t.Policy.AlertOn)
}

// ValidateConditions refuses any attention condition birddog cannot raise.
func ValidateConditions(conditions []string) error {
	for _, c := range conditions {
		if !knownConditions[c] {
			return fmt.Errorf("unknown alert condition %q", c)
		}
	}
	return nil
}

func buildTarget(tw targetWire) (Target, error) {
	if strings.TrimSpace(tw.ID) == "" {
		return Target{}, fmt.Errorf("id is required")
	}
	if !provider.Known(tw.Provider) {
		return Target{}, fmt.Errorf("unknown provider %q", tw.Provider)
	}
	if tw.Attachment.SessionID == "" && tw.Attachment.PID == 0 {
		return Target{}, fmt.Errorf("attachment needs a session_id or a pid")
	}
	if tw.Workspace != "" && !filepath.IsAbs(tw.Workspace) {
		return Target{}, fmt.Errorf("workspace %q must be an absolute path", tw.Workspace)
	}

	policy, err := buildPolicy(tw.Policy)
	if err != nil {
		return Target{}, err
	}

	target := Target{
		ID:         tw.ID,
		Provider:   tw.Provider,
		Attachment: tw.Attachment,
		Workspace:  tw.Workspace,
		Labels:     tw.Labels,
		Policy:     policy,
	}
	if tw.Observations != nil {
		for _, path := range append(append([]string{}, tw.Observations.Files...), tw.Observations.Logs...) {
			if !filepath.IsAbs(path) {
				return Target{}, fmt.Errorf("observed path %q must be absolute", path)
			}
		}
		target.Observations = *tw.Observations
	}
	return target, nil
}

func buildPolicy(pw *policyWire) (Policy, error) {
	p := Policy{
		AlertOn:    append([]string(nil), defaultAlerts...),
		QuietAfter: DefaultQuietAfter,
		IdleGrace:  DefaultIdleGrace,
	}
	if pw == nil {
		return p, nil
	}

	if pw.AlertOn != nil {
		for _, c := range pw.AlertOn {
			if !knownConditions[c] {
				return Policy{}, fmt.Errorf("unknown alert condition %q", c)
			}
		}
		// An explicit list replaces the defaults rather than adding to them,
		// so an operator can narrow what they hear about.
		p.AlertOn = append([]string(nil), pw.AlertOn...)
	}
	if pw.QuietAfterSeconds != nil {
		if *pw.QuietAfterSeconds <= 0 {
			return Policy{}, fmt.Errorf("quiet_after_seconds must be positive")
		}
		p.QuietAfter = time.Duration(*pw.QuietAfterSeconds) * time.Second
	}
	if pw.NoProgressAfterSecs != nil {
		if *pw.NoProgressAfterSecs <= 0 {
			return p, fmt.Errorf("no_progress_after_seconds must be positive")
		}
		p.NoProgressAfter = time.Duration(*pw.NoProgressAfterSecs) * time.Second
	}
	if pw.IdleGraceSeconds != nil {
		if *pw.IdleGraceSeconds < 0 {
			return Policy{}, fmt.Errorf("idle_grace_seconds must not be negative")
		}
		p.IdleGrace = time.Duration(*pw.IdleGraceSeconds) * time.Second
	}
	if pw.ExpectedQuietUntil != nil {
		until, err := time.Parse(time.RFC3339, *pw.ExpectedQuietUntil)
		if err != nil {
			return Policy{}, fmt.Errorf("expected_quiet_until: %w", err)
		}
		p.ExpectedQuietUntil = &until
	}
	p.ExpectedQuietReason = pw.ExpectedQuietReason
	return p, nil
}

// buildRetention validates a retention block, refusing one that bounds nothing.
//
// Both keys or neither. A window without a hold would silently decide how long
// an uncollected terminal outcome survives, and a hold without a window bounds
// something that never happens — either way an operator would have written a
// setting and not got it, which this package refuses on principle.
func buildRetention(rw *retentionWire) (*Retention, error) {
	if rw == nil {
		return nil, nil
	}
	switch {
	case rw.MaxAgeSeconds == nil && rw.HoldUncollectedSeconds == nil:
		return nil, fmt.Errorf("retention needs max_age_seconds and hold_uncollected_seconds, or omit the block to keep everything")
	case rw.MaxAgeSeconds == nil:
		return nil, fmt.Errorf("retention: hold_uncollected_seconds without max_age_seconds bounds nothing")
	case rw.HoldUncollectedSeconds == nil:
		return nil, fmt.Errorf("retention: max_age_seconds requires hold_uncollected_seconds, so how long an uncollected terminal outcome is held is stated rather than assumed")
	}
	if *rw.MaxAgeSeconds <= 0 {
		return nil, fmt.Errorf("retention: max_age_seconds must be positive")
	}
	if *rw.HoldUncollectedSeconds <= 0 {
		return nil, fmt.Errorf("retention: hold_uncollected_seconds must be positive")
	}
	if *rw.MaxAgeSeconds > maxRetentionSeconds {
		return nil, fmt.Errorf("retention: max_age_seconds %d is too large (at most %d)",
			*rw.MaxAgeSeconds, maxRetentionSeconds)
	}
	if *rw.HoldUncollectedSeconds > maxRetentionSeconds {
		return nil, fmt.Errorf("retention: hold_uncollected_seconds %d is too large (at most %d)",
			*rw.HoldUncollectedSeconds, maxRetentionSeconds)
	}
	return &Retention{
		MaxAge:          time.Duration(*rw.MaxAgeSeconds) * time.Second,
		HoldUncollected: time.Duration(*rw.HoldUncollectedSeconds) * time.Second,
	}, nil
}
