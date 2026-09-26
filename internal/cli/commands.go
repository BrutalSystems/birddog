package cli

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	iofs "io/fs"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/BrutalSystems/birddog/internal/config"
	"github.com/BrutalSystems/birddog/internal/daemon"
	"github.com/BrutalSystems/birddog/internal/discover/claude"
	"github.com/BrutalSystems/birddog/internal/discover/codex"
	"github.com/BrutalSystems/birddog/internal/instance"
	"github.com/BrutalSystems/birddog/internal/ipc"
	"github.com/BrutalSystems/birddog/internal/machine"
	"github.com/BrutalSystems/birddog/internal/monitor"
	"github.com/BrutalSystems/birddog/internal/notify"
	// Imported for its side effect: registering the connector makes it
	// configurable. A new route is a new package and one line here.
	_ "github.com/BrutalSystems/birddog/internal/notify/claudeinbox"
	_ "github.com/BrutalSystems/birddog/internal/notify/tincan"
	"github.com/BrutalSystems/birddog/internal/observe"
	"github.com/BrutalSystems/birddog/internal/observe/transcript"
	"github.com/BrutalSystems/birddog/internal/platform/paths"
)

// flags builds a flag set that reports usage errors as errUsage.
func flags(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	return fs
}

func parse(fs *flag.FlagSet, args []string) error {
	if err := fs.Parse(args); err != nil {
		return errUsage
	}
	return nil
}

func emit(w io.Writer, asJSON bool, v any, text func(io.Writer) error) error {
	if asJSON {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(v)
	}
	return text(w)
}

// ---- discover ----

func runDiscover(args []string, stdout io.Writer) error {
	fs := flags("discover")
	asJSON := fs.Bool("json", false, "emit JSON for programmatic use")
	registryDir := fs.String("registry-dir", "", "override the Claude Code session registry directory")
	if err := parse(fs, args); err != nil {
		return err
	}

	registry := *registryDir
	if registry == "" {
		var err error
		if registry, err = claude.DefaultRegistryDir(); err != nil {
			return err
		}
	}

	claudeSessions, err := claude.List(claude.Params{RegistryDir: registry})
	if err != nil {
		// No registry directory means no sessions, which is an answer.
		if !errors.Is(err, iofs.ErrNotExist) {
			return err
		}
		claudeSessions = nil
	}
	rows := FromClaude(claudeSessions)

	codexSessions, err := discoverCodex()
	if err != nil {
		return err
	}
	rows = append(rows, FromCodex(codexSessions)...)

	if *asJSON {
		return RenderJSON(stdout, rows)
	}
	return RenderText(stdout, rows)
}

// discoverCodex lists live Codex threads. Liveness needs no codex process;
// metadata spawns an app-server child only when there is a thread to describe.
func discoverCodex() ([]codex.Session, error) {
	lockDir, err := codex.DefaultLockDir()
	if err != nil {
		return nil, err
	}
	return codex.List(codex.Params{
		LockDir:    lockDir,
		LockHolder: codex.LsofHolder,
		HolderCWD:  codex.HolderCWD,
		Threads: func() ([]codex.Thread, error) {
			client, err := codex.Connect()
			if err != nil {
				return nil, err
			}
			defer client.Close()
			return codex.ListThreads(client)
		},
	})
}

// ---- start ----

// StartResult is what `start` reports, in the shape the handoff asks for: the
// instance id, where its state lives, and how to reach it.
type StartResult struct {
	InstanceID string `json:"instance_id"`
	Name       string `json:"name"`
	PID        int    `json:"pid"`
	SocketPath string `json:"socket_path"`
	DBPath     string `json:"db_path"`
	ConfigPath string `json:"config_path"`
	LogPath    string `json:"log_path,omitempty"`
	Targets    int    `json:"targets"`
	Cursor     int64  `json:"cursor"`
}

func runStart(args []string, stdout io.Writer) error {
	fs := flags("start")
	configPath := fs.String("config", "", "path to the instance configuration")
	asJSON := fs.Bool("json", false, "emit JSON for programmatic use")
	foreground := fs.Bool("foreground", false, "run in this process instead of detaching (for debugging)")
	owner := fs.String("owner", "", "session id to tie this instance to; it stops when that session is gone")
	if err := parse(fs, args); err != nil {
		return err
	}
	if *configPath == "" {
		return fmt.Errorf("--config is required")
	}
	// Validated here as well as in the daemon, and before anything is looked
	// up. Both of these commands spawn the daemon and then only poll for its
	// socket, so a child that dies on a malformed identity reaches the
	// operator as "did not become reachable" after a ten-second wait — a
	// message naming the wrong problem entirely.
	if _, err := machine.FromEnv(); err != nil {
		return err
	}

	abs, err := filepath.Abs(*configPath)
	if err != nil {
		return err
	}

	cfg, err := loadConfig(abs)
	if err != nil {
		return err
	}

	if *owner != "" {
		// Resolved now rather than at first check, so naming a session that
		// does not exist fails the start instead of reaping the instance
		// three passes later.
		if err := checkOwnerExists(*owner); err != nil {
			return err
		}
	}

	id := instance.NewID()
	if *foreground {
		return serveInstance(id, abs, cfg, *owner, stdout)
	}
	return spawnInstanceWithID(id, abs, cfg, *owner, stdout, *asJSON, false)
}

func loadConfig(path string) (*config.Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	return config.Parse(data)
}

// spawnInstanceWithID starts the daemon in its own process and waits until it
// answers. Success is reported only once the instance is actually reachable.
func spawnInstanceWithID(id, configPath string, cfg *config.Config, owner string, stdout io.Writer, asJSON, resumed bool) error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate birddog: %w", err)
	}
	stateDir, err := paths.EnsureStateDir()
	if err != nil {
		return err
	}
	logPath := filepath.Join(stateDir, "logs", id+".log")
	if err := os.MkdirAll(filepath.Dir(logPath), 0o700); err != nil {
		return fmt.Errorf("create log directory: %w", err)
	}
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("open log: %w", err)
	}
	defer logFile.Close()

	serveArgs := []string{"serve", "--instance", id, "--config", configPath}
	if owner != "" {
		serveArgs = append(serveArgs, "--owner", owner)
	}
	cmd := exec.Command(exe, serveArgs...)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	// Its own session, so the instance outlives the shell that started it —
	// monitoring must not end when a terminal closes.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start instance: %w", err)
	}

	socketPath, err := paths.SocketPath(id)
	if err != nil {
		return err
	}
	status, err := waitUntilReachable(socketPath, 10*time.Second)
	if err != nil {
		return fmt.Errorf("instance did not become reachable (see %s): %w", logPath, err)
	}

	dbPath, _ := paths.InstanceDB(id)
	result := StartResult{
		InstanceID: id,
		Name:       cfg.Name,
		PID:        cmd.Process.Pid,
		SocketPath: socketPath,
		DBPath:     dbPath,
		ConfigPath: configPath,
		LogPath:    logPath,
		Targets:    len(cfg.Targets),
		Cursor:     status.Cursor,
	}

	verb := "Started"
	if resumed {
		verb = "Resumed"
	}
	return emit(stdout, asJSON, result, func(w io.Writer) error {
		fmt.Fprintf(w, "%s %s (%s), watching %d target(s).\n", verb, result.InstanceID, result.Name, result.Targets)
		if resumed && result.Cursor > 0 {
			fmt.Fprintf(w, "  Its event history is intact; follow from cursor %d.\n", result.Cursor)
		}
		fmt.Fprintf(w, "  state:  %s\n", result.DBPath)
		fmt.Fprintf(w, "  socket: %s\n", result.SocketPath)
		fmt.Fprintf(w, "  log:    %s\n", result.LogPath)
		fmt.Fprintf(w, "\nFollow it with:\n  birddog events --instance %s --after %d --wait %d\n",
			result.InstanceID, result.Cursor, daemon.MaxWaitSeconds)
		return nil
	})
}

func waitUntilReachable(socketPath string, within time.Duration) (daemon.StatusResult, error) {
	deadline := time.Now().Add(within)
	var lastErr error
	for time.Now().Before(deadline) {
		var status daemon.StatusResult
		if err := ipc.Call(socketPath, "status", nil, &status); err == nil {
			return status, nil
		} else {
			lastErr = err
		}
		time.Sleep(25 * time.Millisecond)
	}
	return daemon.StatusResult{}, lastErr
}

// ---- resume ----

func runResume(args []string, stdout io.Writer) error {
	fs := flags("resume")
	id := fs.String("instance", "", "instance id")
	configPath := fs.String("config", "", "configuration to resume with (default: the one it was started with)")
	asJSON := fs.Bool("json", false, "emit JSON for programmatic use")
	if err := parse(fs, args); err != nil {
		return err
	}
	if *id == "" {
		return fmt.Errorf("--instance is required")
	}

	// Validated here as well as in the daemon, and before anything is looked
	// up. Both of these commands spawn the daemon and then only poll for its
	// socket, so a child that dies on a malformed identity reaches the
	// operator as "did not become reachable" after a ten-second wait — a
	// message naming the wrong problem entirely.
	if _, err := machine.FromEnv(); err != nil {
		return err
	}

	stateDir, err := paths.StateDir()
	if err != nil {
		return err
	}
	rec, ok, err := instance.Load(filepath.Join(stateDir, "registry"), *id)
	if err != nil {
		return err
	}
	if !ok {
		return &ipc.Error{Code: ipc.CodeNotFound, Message: fmt.Sprintf("no instance %q; try `birddog list`", *id)}
	}
	if rec.StoppedAt == nil && instance.ProcessAlive(rec.PID) {
		return fmt.Errorf("instance %s is already running (pid %d)", *id, rec.PID)
	}

	// The configuration it was started with, unless told otherwise. Resuming
	// with a different watch list is allowed, but it is an explicit act.
	path := rec.ConfigPath
	if *configPath != "" {
		if path, err = filepath.Abs(*configPath); err != nil {
			return err
		}
	}
	if path == "" {
		return fmt.Errorf("instance %s has no recorded configuration; pass --config", *id)
	}
	cfg, err := loadConfig(path)
	if err != nil {
		return err
	}

	// The same id means the same database, so the event feed continues across
	// the gap rather than starting over. A consumer's cursor still works.
	return spawnInstanceWithID(*id, path, cfg, rec.Owner, stdout, *asJSON, true)
}

// ---- serve ----

func runServe(args []string, stderr io.Writer) error {
	fs := flags("serve")
	id := fs.String("instance", "", "instance id")
	configPath := fs.String("config", "", "path to the instance configuration")
	owner := fs.String("owner", "", "session id this instance is tied to")
	if err := parse(fs, args); err != nil {
		return err
	}
	if *id == "" || *configPath == "" {
		return fmt.Errorf("--instance and --config are required")
	}

	cfg, err := loadConfig(*configPath)
	if err != nil {
		return err
	}
	return serveInstance(*id, *configPath, cfg, *owner, stderr)
}

// serveInstance runs the observation loop in this process until stopped.
func serveInstance(id, configPath string, cfg *config.Config, owner string, out io.Writer) error {
	stateDir, err := paths.EnsureStateDir()
	if err != nil {
		return err
	}
	if _, err := paths.EnsureInstanceDir(); err != nil {
		return err
	}
	if _, err := paths.EnsureSocketDir(); err != nil {
		return err
	}
	socketPath, err := paths.SocketPath(id)
	if err != nil {
		return err
	}
	dbPath, err := paths.InstanceDB(id)
	if err != nil {
		return err
	}
	lockPath, err := paths.InstanceLock(id)
	if err != nil {
		return err
	}

	observers, err := buildObservers()
	if err != nil {
		return err
	}

	notifier, err := buildNotifier(cfg)
	if err != nil {
		return err
	}

	machineID, err := machine.FromEnv()
	if err != nil {
		return err
	}

	d, err := daemon.Start(daemon.Options{
		Owner:       ownerWatch(owner),
		InstanceID:  id,
		Machine:     machineID,
		Config:      cfg,
		ConfigPath:  configPath,
		SocketPath:  socketPath,
		DBPath:      dbPath,
		LockPath:    lockPath,
		RegistryDir: filepath.Join(stateDir, "registry"),
		Observers:   observers,
		Notifier:    notifier,
	})
	if err != nil {
		return err
	}

	// A terminated instance must still give back its socket, its lock and its
	// registry record, or the next start finds them stranded.
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-signals
		_ = d.Shutdown()
	}()

	fmt.Fprintf(out, "birddog %s watching %d target(s) on %s\n", id, len(cfg.Targets), socketPath)
	d.Wait()
	fmt.Fprintf(out, "birddog %s stopped; watched sessions were not touched\n", id)
	return nil
}

// buildNotifier chooses where this instance's alerts go.
//
// The default is none: alerts stay in the event feed, which is the recovery
// path whatever connector is configured, and the whole answer when none is.
// An unknown kind is refused at startup rather than silently ignored.
func buildNotifier(cfg *config.Config) (notify.Notifier, error) {
	if cfg.Notification == nil {
		return notify.None{}, nil
	}
	return notify.Build(notify.Options{
		Kind:     cfg.Notification.Kind,
		Settings: cfg.Notification.Options,
	})
}

// ownerWatch ties an instance to the session that asked for it.
//
// An instance outlives the shell that started it, which is what lets
// monitoring survive an orchestrator's turn ending — and is why nothing reaps
// it when that orchestrator closes for good. Naming an owner opts into being
// reaped: when the session that wanted the watch is gone, there is nobody
// left to read the answers.
//
// The liveness check is the same one birddog applies to a watched Claude Code
// session: the registry record plus a socket probe plus the process identity,
// so a reused pid cannot keep an instance alive on behalf of a session that
// ended.
func ownerWatch(sessionID string) *daemon.Owner {
	if sessionID == "" {
		return nil
	}
	registryDir, err := claude.DefaultRegistryDir()
	if err != nil {
		return nil
	}
	return &daemon.Owner{
		Description: sessionID,
		Alive: func() bool {
			sessions, err := claude.List(claude.Params{RegistryDir: registryDir})
			if err != nil {
				// Unreadable registry is not evidence the owner has gone.
				// Saying it is alive keeps a transient failure from ending
				// the watch; a genuine absence still reports three times.
				return true
			}
			for _, s := range sessions {
				if s.SessionID == sessionID {
					return s.Live
				}
			}
			return false
		},
	}
}

// checkOwnerExists refuses a start naming a session that is not there, rather
// than starting an instance that reaps itself moments later.
func checkOwnerExists(sessionID string) error {
	registryDir, err := claude.DefaultRegistryDir()
	if err != nil {
		return err
	}
	sessions, err := claude.List(claude.Params{RegistryDir: registryDir})
	if err != nil {
		return fmt.Errorf("check owner session: %w", err)
	}
	for _, s := range sessions {
		if s.SessionID == sessionID {
			return nil
		}
	}
	return fmt.Errorf("no Claude Code session %q to own this instance; `birddog discover` lists them", sessionID)
}

// opencodeRecordsDir is where the birddog opencode plugin publishes. Both
// halves honour BIRDDOG_HOME, so an operator can move them together.
func opencodeRecordsDir() string {
	if home := os.Getenv("BIRDDOG_HOME"); home != "" {
		return filepath.Join(home, "opencode")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".birddog", "opencode")
	}
	return filepath.Join(home, ".birddog", "opencode")
}

// opencodePluginLog is the plugin's diagnostic log, which it keeps beside the
// records rather than among them — birddog sweeps that directory, and a log
// file in it would confuse both halves.
//
// It is the only thing that distinguishes a plugin that is not installed from
// one that is installed on a machine where no opencode session is running.
func opencodePluginLog(recordsDir string) string {
	return filepath.Join(filepath.Dir(recordsDir), "opencode-plugin.log")
}

func buildObservers() (map[string]monitor.Observer, error) {
	registryDir, err := claude.DefaultRegistryDir()
	if err != nil {
		return nil, err
	}
	lockDir, err := codex.DefaultLockDir()
	if err != nil {
		return nil, err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	return observe.Build(observe.Deps{
		ClaudeRegistryDir: registryDir,
		HookStateDir:      hookStateDir(),
		CodexLockDir:      lockDir,
		OpencodeRecords:   opencodeRecordsDir(),
		ProjectsDirs:      transcript.DefaultProjectsDirs(home),
	})
}

// ---- status, events, ack, stop ----

func instanceSocket(id string) (string, error) {
	if id == "" {
		return "", fmt.Errorf("--instance is required")
	}
	stateDir, err := paths.StateDir()
	if err != nil {
		return "", err
	}
	rec, ok, err := instance.Load(filepath.Join(stateDir, "registry"), id)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", &ipc.Error{Code: ipc.CodeNotFound, Message: fmt.Sprintf("no instance %q; try `birddog list`", id)}
	}
	return rec.SocketPath, nil
}

func runStatus(args []string, stdout io.Writer) error {
	fs := flags("status")
	id := fs.String("instance", "", "instance id")
	asJSON := fs.Bool("json", false, "emit JSON for programmatic use")
	if err := parse(fs, args); err != nil {
		return err
	}
	socket, err := instanceSocket(*id)
	if err != nil {
		return err
	}

	var status daemon.StatusResult
	if err := ipc.Call(socket, "status", nil, &status); err != nil {
		return err
	}

	return emit(stdout, *asJSON, status, func(w io.Writer) error {
		return renderStatus(w, status)
	})
}

// renderStatus writes the operator's view of an instance.
func renderStatus(w io.Writer, status daemon.StatusResult) error {
	fmt.Fprintf(w, "%s  (%s)  pid %d  cursor %d", status.InstanceID, status.Name, status.PID, status.Cursor)
	if status.Machine != "" {
		// Only when there is one. In local mode a cursor needs no
		// qualification and saying so would be noise.
		fmt.Fprintf(w, "  on %s", status.Machine)
	}
	fmt.Fprint(w, "\n\n")
	{
		tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "TARGET\tPROVIDER\tSTATUS\tATTENTION")
		for _, t := range status.Targets {
			state := t.Status
			if state == "" {
				state = "unknown"
			}
			if !t.StatusIsCurrent && state != "unknown" {
				state += " (stale)"
			}
			attention := "—"
			if len(t.Incidents) > 0 {
				attention = ""
				for i, inc := range t.Incidents {
					if i > 0 {
						attention += ", "
					}
					attention += inc.Condition
					if inc.AcknowledgedAt != nil {
						attention += " (seen)"
					}
				}
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", t.ID, t.Provider, state, attention)
		}
		if err := tw.Flush(); err != nil {
			return err
		}
		fmt.Fprintf(w, "\nInput-request visibility is reported per target; \"unavailable\" means\nbirddog cannot see requests, not that there are none.\n")
	}
	return nil
}

// eventsOpts holds the events command's flags. Defined once so the set a
// script depends on can be asserted rather than read.
type eventsOpts struct {
	instance *string
	after    *int64
	limit    *int
	wait     *int
	store    *string
	machine  *string
	asJSON   *bool
}

func newEventsFlags() (*flag.FlagSet, *eventsOpts) {
	fs := flags("events")
	o := &eventsOpts{
		instance: fs.String("instance", "", "instance id"),
		after:    fs.Int64("after", 0, "cursor to read from"),
		limit:    fs.Int("limit", 100, "maximum events to return"),
		wait:     fs.Int("wait", 0, fmt.Sprintf("seconds to hold the request open waiting for new events (max %d)", daemon.MaxWaitSeconds)),
		store:    fs.String("store", "", "store the cursor came from; a replaced store fails rather than reporting no events"),
		machine:  fs.String("machine", "", "machine the cursor came from; a cursor from another machine fails rather than being answered from this one's history"),
		asJSON:   fs.Bool("json", false, "emit JSON for programmatic use"),
	}
	return fs, o
}

// eventsFlagSet exposes the flag set alone, for asserting the surface.
func eventsFlagSet() *flag.FlagSet {
	fs, _ := newEventsFlags()
	return fs
}

func runEvents(args []string, stdout io.Writer) error {
	fs, o := newEventsFlags()
	if err := parse(fs, args); err != nil {
		return err
	}
	id, after, limit, wait := o.instance, o.after, o.limit, o.wait
	store, asJSON := o.store, o.asJSON
	socket, err := instanceSocket(*id)
	if err != nil {
		return err
	}

	var result daemon.EventsResult
	params := daemon.EventsParams{
		After: *after, Limit: *limit, WaitSeconds: *wait,
		Store: *store, Machine: *o.machine,
	}
	if err := ipc.Call(socket, "events", params, &result); err != nil {
		return err
	}

	return emit(stdout, *asJSON, result, func(w io.Writer) error {
		if len(result.Events) == 0 {
			fmt.Fprintf(w, "No new events. Cursor %d.\n", result.Cursor)
			return nil
		}
		tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "CURSOR\tAT\tTARGET\tTYPE\tSTATUS")
		for _, e := range result.Events {
			status := e.Status
			if status == "" {
				status = "—"
			}
			fmt.Fprintf(tw, "%d\t%s\t%s\t%s\t%s\n",
				e.Cursor, e.At.Format(time.RFC3339), e.TargetID, e.Type, status)
		}
		if err := tw.Flush(); err != nil {
			return err
		}
		fmt.Fprintf(w, "\nFollow from cursor %d.\n", result.Cursor)
		return nil
	})
}

func runAck(args []string, stdout io.Writer) error {
	fs := flags("ack")
	id := fs.String("instance", "", "instance id")
	incident := fs.Int64("incident", 0, "incident id, as shown by status")
	key := fs.String("idempotency-key", "", "retry-safe key: repeating it returns the original result")
	asJSON := fs.Bool("json", false, "emit JSON for programmatic use")
	if err := parse(fs, args); err != nil {
		return err
	}
	if *incident == 0 {
		return fmt.Errorf("--incident is required")
	}
	socket, err := instanceSocket(*id)
	if err != nil {
		return err
	}

	var result daemon.AckResult
	if err := ipc.Call(socket, "ack", daemon.AckParams{IncidentID: *incident, IdempotencyKey: *key}, &result); err != nil {
		return err
	}

	return emit(stdout, *asJSON, result, func(w io.Writer) error {
		fmt.Fprintf(w, "Incident %d recorded as seen.\n", result.IncidentID)
		fmt.Fprintf(w, "Nothing was approved, resolved or sent: the observed condition is unchanged.\n")
		return nil
	})
}

func runStop(args []string, stdout io.Writer) error {
	fs := flags("stop")
	id := fs.String("instance", "", "instance id")
	asJSON := fs.Bool("json", false, "emit JSON for programmatic use")
	if err := parse(fs, args); err != nil {
		return err
	}
	socket, err := instanceSocket(*id)
	if err != nil {
		return err
	}

	var result daemon.StopResult
	if err := ipc.Call(socket, "stop", nil, &result); err != nil {
		return err
	}

	// The instance replies before it shuts down, so that the caller hears the
	// request was accepted rather than seeing the connection vanish. Wait for
	// it to actually go: a caller that stops and immediately resumes, or
	// starts something else, must not race its own shutdown.
	if err := waitUntilStopped(socket, 10*time.Second); err != nil {
		return err
	}

	return emit(stdout, *asJSON, result, func(w io.Writer) error {
		fmt.Fprintf(w, "Stopped %s.\n", *id)
		fmt.Fprintf(w, "Every watched session is still running; birddog held nothing of theirs.\n")
		return nil
	})
}

// waitUntilStopped blocks until the instance stops answering.
func waitUntilStopped(socketPath string, within time.Duration) error {
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if err := ipc.Call(socketPath, "status", nil, &daemon.StatusResult{}); err != nil {
			return nil // no longer answering: it is down
		}
		time.Sleep(20 * time.Millisecond)
	}
	return fmt.Errorf("instance is still answering %s after being asked to stop", socketPath)
}

// ---- list ----

// InstanceRow is one instance as `list` reports it.
//
// Separate from the stored record on purpose: Running is computed fresh on
// every listing and must never be written down, because a stored answer to
// "is it running" is stale the moment it is written. Keeping it out of the
// record while emitting it here is the distinction the storage tag alone
// could not make.
type InstanceRow struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	PID        int    `json:"pid"`
	Running    bool   `json:"running"`
	Owner      string `json:"owner,omitempty"`
	SocketPath string `json:"socket_path,omitempty"`
	ConfigPath string `json:"config_path,omitempty"`
	DBPath     string `json:"db_path,omitempty"`
	StartedAt  string `json:"started_at"`
	StoppedAt  string `json:"stopped_at,omitempty"`
}

func listRows(records []instance.Record) []InstanceRow {
	rows := make([]InstanceRow, 0, len(records))
	for _, r := range records {
		row := InstanceRow{
			ID: r.ID, Name: r.Name, PID: r.PID, Running: r.Running, Owner: r.Owner,
			SocketPath: r.SocketPath, ConfigPath: r.ConfigPath, DBPath: r.DBPath,
			StartedAt: r.StartedAt.UTC().Format(time.RFC3339),
		}
		if r.StoppedAt != nil {
			row.StoppedAt = r.StoppedAt.UTC().Format(time.RFC3339)
		}
		rows = append(rows, row)
	}
	return rows
}

func runList(args []string, stdout io.Writer) error {
	fs := flags("list")
	asJSON := fs.Bool("json", false, "emit JSON for programmatic use")
	if err := parse(fs, args); err != nil {
		return err
	}
	stateDir, err := paths.StateDir()
	if err != nil {
		return err
	}

	records, err := instance.ListWithLiveness(filepath.Join(stateDir, "registry"), instance.ProcessAlive)
	if err != nil {
		return err
	}

	return emit(stdout, *asJSON, map[string]any{"instances": listRows(records)}, func(w io.Writer) error {
		if len(records) == 0 {
			fmt.Fprintln(w, "No monitoring instances.")
			return nil
		}
		tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "INSTANCE\tNAME\tPID\tRUNNING\tOWNER\tSTARTED")
		for _, r := range records {
			running := "no"
			if r.Running {
				running = "yes"
			}
			owner := r.Owner
			if owner == "" {
				owner = "—"
			} else if len(owner) > 12 {
				owner = owner[:12] + "…"
			}
			fmt.Fprintf(tw, "%s\t%s\t%d\t%s\t%s\t%s\n",
				r.ID, r.Name, r.PID, running, owner, r.StartedAt.Format(time.RFC3339))
		}
		return tw.Flush()
	})
}
