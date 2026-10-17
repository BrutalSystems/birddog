package config

import "testing"

func TestParseTargetAppliesTheFilesDefaults(t *testing.T) {
	got, err := ParseTarget([]byte(`{"id": "w", "provider": "claude", "attachment": {"session_id": "s"},
		"policy": {"quiet_after_seconds": 600}}`))
	if err != nil {
		t.Fatalf("ParseTarget: %v", err)
	}
	if got.Policy.QuietAfter.Seconds() != 600 || got.Policy.IdleGrace != DefaultIdleGrace || len(got.Policy.AlertOn) != len(defaultAlerts) {
		t.Errorf("policy = %+v, want the file's threshold and the defaults for the rest", got.Policy)
	}
}

func TestParseTargetRefusesWhatAFileWould(t *testing.T) {
	for name, body := range map[string]string{
		"unknown field":    `{"id": "w", "provider": "claude", "attachment": {"session_id": "s"}, "polcy": {}}`,
		"unknown provider": `{"id": "w", "provider": "nope", "attachment": {"session_id": "s"}}`,
		"zero quiet":       `{"id": "w", "provider": "claude", "attachment": {"session_id": "s"}, "policy": {"quiet_after_seconds": 0}}`,
	} {
		if _, err := ParseTarget([]byte(body)); err == nil {
			t.Errorf("%s: ParseTarget = nil error", name)
		}
	}
}

func TestFillDefaultsKeepsAnExplicitEmptyAlertList(t *testing.T) {
	if p := FillDefaults(Policy{AlertOn: []string{}}); len(p.AlertOn) != 0 || p.QuietAfter != DefaultQuietAfter {
		t.Errorf("FillDefaults = %+v, want the empty list kept and the quiet default filled", p)
	}
	if p := FillDefaults(Policy{}); len(p.AlertOn) != len(defaultAlerts) {
		t.Errorf("FillDefaults(nil alerts) = %+v, want the defaults", p.AlertOn)
	}
}

func TestFollowFileIsReadFromTheFile(t *testing.T) {
	cfg, err := Parse([]byte(`{"schema_version": 1, "name": "x", "follow_file": true, "targets": []}`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if !cfg.FollowFile {
		t.Error("FollowFile = false, want true")
	}
}

func TestADeadlineIsReadInEitherForm(t *testing.T) {
	at, err := ParseTarget([]byte(`{"id": "w", "provider": "claude", "attachment": {"session_id": "s"},
		"policy": {"deadline": "2026-10-17T12:00:00Z"}}`))
	if err != nil {
		t.Fatalf("ParseTarget(deadline): %v", err)
	}
	if at.Policy.Deadline == nil || at.Policy.Deadline.Format("15:04") != "12:00" || !at.Policy.AlertsOn(ConditionOverdue) {
		t.Errorf("policy = %+v, want the deadline and overdue in the default alert list", at.Policy)
	}

	after, err := ParseTarget([]byte(`{"id": "w", "provider": "claude", "attachment": {"session_id": "s"},
		"policy": {"deadline_after_seconds": 2700}}`))
	if err != nil {
		t.Fatalf("ParseTarget(deadline_after_seconds): %v", err)
	}
	// A file has no clock: the instance resolves it when it takes the target on.
	if after.Policy.DeadlineAfter.Minutes() != 45 || after.Policy.Deadline != nil {
		t.Errorf("policy = %+v, want 45m unresolved", after.Policy)
	}
}

func TestADeadlineThatCouldNotAlertIsRefused(t *testing.T) {
	for name, body := range map[string]string{
		"both forms":       `{"deadline": "2026-10-17T12:00:00Z", "deadline_after_seconds": 60}`,
		"zero after":       `{"deadline_after_seconds": 0}`,
		"bad time":         `{"deadline": "tomorrow"}`,
		"overdue excluded": `{"alert_on": ["exit"], "deadline_after_seconds": 60}`,
	} {
		_, err := ParseTarget([]byte(`{"id": "w", "provider": "claude", "attachment": {"session_id": "s"}, "policy": ` + body + `}`))
		if err == nil {
			t.Errorf("%s: ParseTarget = nil error", name)
		}
	}
}
