package config

import (
	"strings"
	"testing"
	"time"
)

const minimal = `{
  "schema_version": 1,
  "name": "checkout-refactor",
  "targets": [
    {"id": "worker-1", "provider": "claude", "attachment": {"kind": "existing-session", "session_id": "abc"}}
  ]
}`

func TestParseAcceptsAMinimalConfig(t *testing.T) {
	cfg, err := Parse([]byte(minimal))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if cfg.Name != "checkout-refactor" {
		t.Errorf("Name = %q", cfg.Name)
	}
	if len(cfg.Targets) != 1 {
		t.Fatalf("got %d targets, want 1", len(cfg.Targets))
	}
	if cfg.Targets[0].ID != "worker-1" || cfg.Targets[0].Provider != "claude" {
		t.Errorf("target = %+v", cfg.Targets[0])
	}
}

// Defaults come from the handoff: five minutes quiet, thirty seconds idle.
func TestParseAppliesDocumentedDefaults(t *testing.T) {
	cfg, err := Parse([]byte(minimal))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	p := cfg.Targets[0].Policy
	if p.QuietAfter != 5*time.Minute {
		t.Errorf("QuietAfter = %v, want 5m", p.QuietAfter)
	}
	if p.IdleGrace != 30*time.Second {
		t.Errorf("IdleGrace = %v, want 30s", p.IdleGrace)
	}
}

// An unknown field is far more likely a typo than an intention, and silently
// ignoring it means a policy the operator wrote never takes effect.
func TestParseRejectsUnknownTopLevelField(t *testing.T) {
	_, err := Parse([]byte(`{"schema_version":1,"name":"x","notifcation":{"kind":"none"},"targets":[]}`))
	if err == nil {
		t.Fatal("Parse = nil error for an unknown field, want it refused")
	}
	if !strings.Contains(err.Error(), "notifcation") {
		t.Errorf("error = %v, want it to name the offending field", err)
	}
}

func TestParseRejectsUnknownTargetField(t *testing.T) {
	_, err := Parse([]byte(`{"schema_version":1,"name":"x","targets":[
		{"id":"w","provider":"claude","attachment":{"kind":"existing-session","session_id":"a"},"quiet_after":5}]}`))
	if err == nil {
		t.Fatal("Parse = nil error for an unknown target field, want it refused")
	}
}

func TestParseRejectsUnsupportedSchemaVersion(t *testing.T) {
	_, err := Parse([]byte(`{"schema_version":2,"name":"x","targets":[]}`))
	if err == nil {
		t.Fatal("Parse = nil error for schema_version 2, want it refused")
	}
	if !strings.Contains(err.Error(), "schema_version") {
		t.Errorf("error = %v, want it to name the version", err)
	}
}

func TestParseRequiresSchemaVersion(t *testing.T) {
	if _, err := Parse([]byte(`{"name":"x","targets":[]}`)); err == nil {
		t.Error("Parse = nil error with no schema_version, want it required")
	}
}

func TestParseRejectsDuplicateTargetIDs(t *testing.T) {
	_, err := Parse([]byte(`{"schema_version":1,"name":"x","targets":[
		{"id":"w","provider":"claude","attachment":{"kind":"existing-session","session_id":"a"}},
		{"id":"w","provider":"codex","attachment":{"kind":"existing-session","session_id":"b"}}]}`))
	if err == nil {
		t.Fatal("Parse = nil error for duplicate target ids, want it refused")
	}
	if !strings.Contains(err.Error(), "w") {
		t.Errorf("error = %v, want it to name the duplicate", err)
	}
}

func TestParseRejectsUnknownProvider(t *testing.T) {
	_, err := Parse([]byte(`{"schema_version":1,"name":"x","targets":[
		{"id":"w","provider":"cursor","attachment":{"kind":"existing-session","session_id":"a"}}]}`))
	if err == nil {
		t.Error("Parse = nil error for an unknown provider, want it refused")
	}
}

// A relative workspace depends on where birddog happened to be started, which
// for a daemon outliving its shell is never what the operator meant.
func TestParseRejectsRelativeWorkspacePath(t *testing.T) {
	_, err := Parse([]byte(`{"schema_version":1,"name":"x","targets":[
		{"id":"w","provider":"claude","attachment":{"kind":"existing-session","session_id":"a"},"workspace":"./repo"}]}`))
	if err == nil {
		t.Error("Parse = nil error for a relative workspace, want an absolute path required")
	}
}

func TestParseRejectsUnknownAlertCondition(t *testing.T) {
	_, err := Parse([]byte(`{"schema_version":1,"name":"x","targets":[
		{"id":"w","provider":"claude","attachment":{"kind":"existing-session","session_id":"a"},
		 "policy":{"alert_on":["idle","stalled"]}}]}`))
	if err == nil {
		t.Fatal("Parse = nil error for an unknown alert condition, want it refused")
	}
	if !strings.Contains(err.Error(), "stalled") {
		t.Errorf("error = %v, want it to name the condition", err)
	}
}

// Labels are correlation metadata and nothing else. birddog must not read
// meaning into them, so anything JSON allows as a string pair is accepted.
func TestParseKeepsLabelsOpaque(t *testing.T) {
	cfg, err := Parse([]byte(`{"schema_version":1,"name":"x","targets":[
		{"id":"w","provider":"claude","attachment":{"kind":"existing-session","session_id":"a"},
		 "labels":{"task_id":"T-42","anything":"at all"}}]}`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	l := cfg.Targets[0].Labels
	if l["task_id"] != "T-42" || l["anything"] != "at all" {
		t.Errorf("labels = %v", l)
	}
}

func TestParseReadsExpectedQuietOverride(t *testing.T) {
	cfg, err := Parse([]byte(`{"schema_version":1,"name":"x","targets":[
		{"id":"w","provider":"claude","attachment":{"kind":"existing-session","session_id":"a"},
		 "policy":{"expected_quiet_until":"2026-09-21T18:00:00Z","expected_quiet_reason":"running tests"}}]}`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	p := cfg.Targets[0].Policy
	if p.ExpectedQuietUntil == nil {
		t.Fatal("ExpectedQuietUntil not parsed")
	}
	if got := p.ExpectedQuietUntil.UTC().Format(time.RFC3339); got != "2026-09-21T18:00:00Z" {
		t.Errorf("ExpectedQuietUntil = %v", got)
	}
	if p.ExpectedQuietReason != "running tests" {
		t.Errorf("ExpectedQuietReason = %q", p.ExpectedQuietReason)
	}
}

func TestParseRequiresASessionIdentity(t *testing.T) {
	_, err := Parse([]byte(`{"schema_version":1,"name":"x","targets":[
		{"id":"w","provider":"claude","attachment":{"kind":"existing-session"}}]}`))
	if err == nil {
		t.Error("Parse = nil error with no session identity, want it required")
	}
}

// Validation is all-or-nothing: a config with one bad target must not be
// partially applied, leaving an instance in a state the operator never wrote.
func TestParseRejectsTheWholeConfigWhenOneTargetIsInvalid(t *testing.T) {
	cfg, err := Parse([]byte(`{"schema_version":1,"name":"x","targets":[
		{"id":"good","provider":"claude","attachment":{"kind":"existing-session","session_id":"a"}},
		{"id":"bad","provider":"nonsense","attachment":{"kind":"existing-session","session_id":"b"}}]}`))
	if err == nil {
		t.Fatal("Parse = nil error, want the whole config refused")
	}
	if cfg != nil {
		t.Error("Parse returned a config alongside an error — nothing may be applied")
	}
}

func TestParseRejectsMalformedJSON(t *testing.T) {
	if _, err := Parse([]byte(`{"schema_version":1,`)); err == nil {
		t.Error("Parse = nil error for malformed JSON")
	}
}

func TestAlertEnabledDefaultsToTheDocumentedSet(t *testing.T) {
	cfg, _ := Parse([]byte(minimal))
	p := cfg.Targets[0].Policy
	for _, c := range []string{ConditionInputRequested, ConditionExit, ConditionObservationLost} {
		if !p.AlertsOn(c) {
			t.Errorf("AlertsOn(%q) = false, want it enabled by default", c)
		}
	}
}

// Idle fires at the end of every Claude Code turn, so a session simply waiting
// on its human would alert once per turn. It is opt-in.
func TestIdleAlertsAreOffByDefault(t *testing.T) {
	cfg, _ := Parse([]byte(minimal))
	if cfg.Targets[0].Policy.AlertsOn(ConditionIdle) {
		t.Error("AlertsOn(idle) = true by default — that alerts once per turn on Claude Code")
	}
}

func TestExplicitAlertListReplacesTheDefaults(t *testing.T) {
	cfg, err := Parse([]byte(`{"schema_version":1,"name":"x","targets":[
		{"id":"w","provider":"claude","attachment":{"kind":"existing-session","session_id":"a"},
		 "policy":{"alert_on":["exit"]}}]}`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	p := cfg.Targets[0].Policy
	if !p.AlertsOn(ConditionExit) {
		t.Error("AlertsOn(exit) = false, want true")
	}
	if p.AlertsOn(ConditionQuiet) {
		t.Error("AlertsOn(quiet) = true, want false — an explicit list replaces the defaults")
	}
}

func TestParseReadsANotificationRoute(t *testing.T) {
	cfg, err := Parse([]byte(`{"schema_version":1,"name":"x",
		"notification":{"kind":"claude-inbox","options":{"session_id":"abc"}},
		"targets":[]}`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if cfg.Notification == nil {
		t.Fatal("notification not parsed")
	}
	if cfg.Notification.Kind != "claude-inbox" {
		t.Errorf("Kind = %q", cfg.Notification.Kind)
	}
	if cfg.Notification.Options["session_id"] != "abc" {
		t.Errorf("Options = %v", cfg.Notification.Options)
	}
}

func TestParseRejectsUnknownNotificationFields(t *testing.T) {
	_, err := Parse([]byte(`{"schema_version":1,"name":"x",
		"notification":{"kind":"none","recipient":"someone"},"targets":[]}`))
	if err == nil {
		t.Error("Parse = nil error for an unknown notification field")
	}
}

func TestNotificationDefaultsToNone(t *testing.T) {
	cfg, err := Parse([]byte(minimal))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if cfg.Notification != nil && cfg.Notification.Kind != "" && cfg.Notification.Kind != "none" {
		t.Errorf("Notification = %+v, want none by default", cfg.Notification)
	}
}

func TestRetentionIsAbsentByDefault(t *testing.T) {
	cfg, err := Parse([]byte(`{
		"schema_version": 1,
		"name": "n",
		"targets": []
	}`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if cfg.Retention != nil {
		t.Errorf("Retention = %+v, want nil: keeping everything is the default", cfg.Retention)
	}
}

func TestRetentionParsesBothKeys(t *testing.T) {
	cfg, err := Parse([]byte(`{
		"schema_version": 1,
		"name": "n",
		"retention": {"max_age_seconds": 604800, "hold_uncollected_seconds": 86400},
		"targets": []
	}`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if cfg.Retention == nil {
		t.Fatal("Retention = nil, want set")
	}
	if cfg.Retention.MaxAge != 7*24*time.Hour {
		t.Errorf("MaxAge = %v, want 168h", cfg.Retention.MaxAge)
	}
	if cfg.Retention.HoldUncollected != 24*time.Hour {
		t.Errorf("HoldUncollected = %v, want 24h", cfg.Retention.HoldUncollected)
	}
}

func TestRetentionRefusesAnUnboundedWindow(t *testing.T) {
	cases := []struct {
		name, body, wantSubstring string
	}{{
		name:          "max_age without hold",
		body:          `{"max_age_seconds": 100}`,
		wantSubstring: "hold_uncollected_seconds",
	}, {
		name:          "hold without max_age",
		body:          `{"hold_uncollected_seconds": 100}`,
		wantSubstring: "bounds nothing",
	}, {
		name:          "empty object",
		body:          `{}`,
		wantSubstring: "max_age_seconds",
	}, {
		name:          "zero max_age",
		body:          `{"max_age_seconds": 0, "hold_uncollected_seconds": 100}`,
		wantSubstring: "must be positive",
	}, {
		name:          "negative max_age",
		body:          `{"max_age_seconds": -1, "hold_uncollected_seconds": 100}`,
		wantSubstring: "must be positive",
	}, {
		name:          "zero hold",
		body:          `{"max_age_seconds": 100, "hold_uncollected_seconds": 0}`,
		wantSubstring: "must be positive",
	}, {
		name:          "negative hold",
		body:          `{"max_age_seconds": 100, "hold_uncollected_seconds": -1}`,
		wantSubstring: "must be positive",
	}, {
		// The hold is applied as arithmetic on millisecond timestamps. A value
		// this size wraps it, and a wrapped hold holds nothing — which would
		// prune the terminal outcome the hold exists to protect, silently.
		name:          "hold large enough to wrap millisecond arithmetic",
		body:          `{"max_age_seconds": 100, "hold_uncollected_seconds": 9223372036854775}`,
		wantSubstring: "too large",
	}, {
		name:          "max_age large enough to wrap millisecond arithmetic",
		body:          `{"max_age_seconds": 9223372036854775, "hold_uncollected_seconds": 100}`,
		wantSubstring: "too large",
	}}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := Parse([]byte(`{
				"schema_version": 1, "name": "n",
				"retention": ` + tc.body + `,
				"targets": []
			}`))
			if err == nil {
				t.Fatalf("Parse succeeded, want refusal; got %+v", cfg.Retention)
			}
			if cfg != nil {
				t.Errorf("config = %+v, want nil on error", cfg)
			}
			if !strings.Contains(err.Error(), tc.wantSubstring) {
				t.Errorf("error %q does not mention %q", err, tc.wantSubstring)
			}
		})
	}
}
