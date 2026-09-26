package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BrutalSystems/birddog/internal/daemon"
	"github.com/BrutalSystems/birddog/internal/machine"
)

// The flag exists and is documented, because a script driving birddog through
// the CLI is a consumer like any other and cannot qualify its cursor without
// it.
func TestEventsAcceptsAMachineFlag(t *testing.T) {
	f := eventsFlagSet().Lookup("machine")
	if f == nil {
		t.Fatal("events has no --machine flag")
	}
	if f.Usage == "" {
		t.Error("--machine has no usage text")
	}
}

// Text output names the machine when there is one, so an operator reading a
// cursor in a cross-machine setup can see which host it belongs to.
func TestStatusTextNamesTheMachine(t *testing.T) {
	var out strings.Builder
	err := renderStatus(&out, daemon.StatusResult{
		InstanceID: "bd-1", Name: "work", PID: 42, Cursor: 7,
		Machine: "ferry:7c3a91b2",
	})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if !strings.Contains(out.String(), "ferry:7c3a91b2") {
		t.Errorf("status text does not name the machine:\n%s", out.String())
	}
}

// In local mode it says nothing rather than saying something empty.
func TestStatusTextSaysNothingAboutMachineInLocalMode(t *testing.T) {
	var out strings.Builder
	if err := renderStatus(&out, daemon.StatusResult{
		InstanceID: "bd-1", Name: "work", PID: 42, Cursor: 7,
	}); err != nil {
		t.Fatalf("render: %v", err)
	}
	if strings.Contains(out.String(), "machine") {
		t.Errorf("status text mentions a machine in local mode:\n%s", out.String())
	}
}

// A malformed identity has to fail the start, with the reason. The daemon
// validates it too, but `start` spawns that daemon and only polls for its
// socket — so a child that dies on a bad value surfaces to the operator as
// "no instance is listening", which names the wrong problem entirely.
//
// The same reasoning as the owner check beside it: resolve it now rather than
// discovering it later, where the message no longer points at the cause.
func TestStartRefusesAMalformedMachineIdentityBeforeSpawning(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("BIRDDOG_STATE_DIR", filepath.Join(dir, "state"))
	t.Setenv(machine.EnvVar, "has a space")

	cfgPath := filepath.Join(dir, "config.json")
	cfg := `{"schema_version":1,"name":"t","targets":[` +
		`{"id":"w","provider":"fake","attachment":{"kind":"existing-session","session_id":"` +
		filepath.Join(dir, "w.json") + `"},"policy":{"alert_on":["exit"]}}]}`
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	var out strings.Builder
	err := runStart([]string{"--config", cfgPath}, &out)
	if err == nil {
		t.Fatal("runStart = nil, want an error naming the malformed identity")
	}
	if !strings.Contains(err.Error(), machine.EnvVar) {
		t.Errorf("error %q does not name %s; an operator cannot tell what to fix", err, machine.EnvVar)
	}
}

// resume spawns the same daemon start does, so it inherits the same problem:
// a child that dies on a malformed identity is reported as "did not become
// reachable" after a ten-second wait, naming the wrong cause. Fixing one path
// and not its sibling leaves the defect in the command an operator reaches for
// after a restart.
func TestResumeRefusesAMalformedMachineIdentityBeforeSpawning(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("BIRDDOG_STATE_DIR", filepath.Join(dir, "state"))
	t.Setenv(machine.EnvVar, "has a space")

	err := runResume([]string{"--instance", "bd-nosuchinstance"}, &strings.Builder{})
	if err == nil {
		t.Fatal("runResume = nil, want an error")
	}
	if !strings.Contains(err.Error(), machine.EnvVar) {
		t.Errorf("error %q does not name %s; it should be refused before anything else is looked up", err, machine.EnvVar)
	}
}
