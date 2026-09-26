package observe

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/BrutalSystems/birddog/internal/config"
	"github.com/BrutalSystems/birddog/internal/policy"
)

func fakeTarget(path string) config.Target {
	return config.Target{
		ID: "worker-1", Provider: "fake",
		Attachment: config.Attachment{Kind: "existing-session", SessionID: path},
	}
}

func writeState(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write state: %v", err)
	}
}

func TestFakeObserverReadsStateFromItsFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "worker.json")
	writeState(t, path, `{"status":"active","live":true,"identity":"run-1","last_activity_at":"2026-09-21T12:00:00Z"}`)

	got, err := (&Fake{}).Observe(fakeTarget(path))
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if got.Status != policy.StatusActive || !got.StatusKnown || !got.Live {
		t.Errorf("observation = %+v", got.Observation)
	}
	if got.SessionIdentity != "run-1" {
		t.Errorf("SessionIdentity = %q", got.SessionIdentity)
	}
	want := time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)
	if !got.LastActivityAt.Equal(want) {
		t.Errorf("LastActivityAt = %v, want %v", got.LastActivityAt, want)
	}
}

func TestFakeObserverSupportsEveryStateTheSmokeTestExercises(t *testing.T) {
	for _, status := range []string{
		policy.StatusActive, policy.StatusIdle, policy.StatusWaitingInput,
		policy.StatusRunningTool, policy.StatusExited,
	} {
		t.Run(status, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "w.json")
			writeState(t, path, `{"status":"`+status+`","live":true,"identity":"r"}`)

			got, err := (&Fake{}).Observe(fakeTarget(path))
			if err != nil {
				t.Fatalf("Observe: %v", err)
			}
			if got.Status != status || !got.StatusKnown {
				t.Errorf("Status = %q known=%v, want %q", got.Status, got.StatusKnown, status)
			}
		})
	}
}

// A removed state file stands for a worker that disconnected: nothing can be
// observed, and nothing is claimed about why.
func TestFakeObserverTreatsAMissingFileAsUnobservable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gone.json")

	got, err := (&Fake{}).Observe(fakeTarget(path))
	if err == nil {
		t.Fatal("Observe = nil error for a missing state file")
	}
	if got.Live || got.StatusKnown {
		t.Error("a missing state file produced a confident observation")
	}
	if got.Status == policy.StatusExited {
		t.Error("a missing state file was reported as an exit — nothing verified that")
	}
}

func TestFakeObserverRejectsAnUnknownStatus(t *testing.T) {
	path := filepath.Join(t.TempDir(), "w.json")
	writeState(t, path, `{"status":"inventing-things","live":true,"identity":"r"}`)

	got, err := (&Fake{}).Observe(fakeTarget(path))
	if err == nil {
		t.Fatal("Observe = nil error for an unknown status")
	}
	if got.StatusKnown {
		t.Error("an unrecognised status was reported as known")
	}
}

func TestFakeObserverRejectsMalformedState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "w.json")
	writeState(t, path, `{"status":`)

	if _, err := (&Fake{}).Observe(fakeTarget(path)); err == nil {
		t.Error("Observe = nil error for malformed state")
	}
}

func TestFakeObserverRequiresAnAbsolutePath(t *testing.T) {
	if _, err := (&Fake{}).Observe(fakeTarget("relative/path.json")); err == nil {
		t.Error("Observe = nil error for a relative state path")
	}
}

// Review Focus 4. parseFakeTime returns time.Now() for an empty string, so the
// parsed value is never zero and cannot decide this. The rule reads the raw field.
func TestFakeWithoutAnActivityStampReportsUnavailableResolution(t *testing.T) {
	path := filepath.Join(t.TempDir(), "worker.json")
	writeState(t, path, `{"status":"idle","live":true,"identity":"run-1"}`)

	got, err := (&Fake{}).Observe(fakeTarget(path))
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if got.ActivityResolution != policy.ResolutionUnavailable {
		t.Errorf("ActivityResolution = %q, want unavailable: the harness stated no timestamp",
			got.ActivityResolution)
	}
}

func TestFakeWithAnActivityStampReportsActivityResolution(t *testing.T) {
	path := filepath.Join(t.TempDir(), "worker.json")
	writeState(t, path, `{"status":"idle","live":true,"identity":"run-1","last_activity_at":"2026-09-21T12:00:00Z"}`)

	got, err := (&Fake{}).Observe(fakeTarget(path))
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if got.ActivityResolution != policy.ResolutionActivity {
		t.Errorf("ActivityResolution = %q, want activity", got.ActivityResolution)
	}
}
