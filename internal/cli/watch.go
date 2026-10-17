package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/BrutalSystems/birddog/internal/config"
	"github.com/BrutalSystems/birddog/internal/daemon"
	"github.com/BrutalSystems/birddog/internal/ipc"
)

func runWatch(args []string, stdout io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("watch needs a subcommand: add, remove, update or sync")
	}
	switch args[0] {
	case "add":
		return runWatchAdd(args[1:], stdout)
	case "remove":
		return runWatchRemove(args[1:], stdout)
	case "update":
		return runWatchUpdate(args[1:], stdout)
	case "sync":
		return runWatchSync(args[1:], stdout)
	default:
		return fmt.Errorf("unknown watch subcommand %q", args[0])
	}
}

func runWatchAdd(args []string, stdout io.Writer) error {
	fs := flags("watch add")
	id := fs.String("instance", "", "instance id")
	key := fs.String("idempotency-key", "", "retry-safe key: repeating it returns the original result")
	file := fs.String("file", "", "JSON file describing the target")
	asJSON := fs.Bool("json", false, "emit JSON for programmatic use")
	if err := parse(fs, args); err != nil {
		return err
	}
	if *file == "" {
		return fmt.Errorf("--file is required")
	}
	socket, err := instanceSocket(*id)
	if err != nil {
		return err
	}

	data, err := os.ReadFile(*file)
	if err != nil {
		return fmt.Errorf("read target: %w", err)
	}
	// Parsed here only to refuse a bad file before anything is sent and to
	// name the target in the reply. The instance parses the same bytes
	// itself: thresholds do not survive the in-memory encoding.
	target, err := config.ParseTarget(data)
	if err != nil {
		return err
	}

	var res daemon.WatchResult
	if err := ipc.Call(socket, "watch.add", daemon.WatchAddParams{
		// Both forms: an instance started by an earlier build reads only
		// Target, and must keep answering until it is restarted.
		Spec: data, Target: target, IdempotencyKey: *key,
	}, &res); err != nil {
		return err
	}
	return emit(stdout, *asJSON, res, func(w io.Writer) error {
		fmt.Fprintf(w, "Watching %s. %d target(s), revision %d.\n", target.ID, res.Targets, res.ConfigRevision)
		return nil
	})
}

func runWatchRemove(args []string, stdout io.Writer) error {
	fs := flags("watch remove")
	id := fs.String("instance", "", "instance id")
	key := fs.String("idempotency-key", "", "retry-safe key: repeating it returns the original result")
	target := fs.String("target", "", "target id to stop watching")
	asJSON := fs.Bool("json", false, "emit JSON for programmatic use")
	if err := parse(fs, args); err != nil {
		return err
	}
	if *target == "" {
		return fmt.Errorf("--target is required")
	}
	socket, err := instanceSocket(*id)
	if err != nil {
		return err
	}

	var res daemon.WatchResult
	if err := ipc.Call(socket, "watch.remove", daemon.WatchRemoveParams{TargetID: *target, IdempotencyKey: *key}, &res); err != nil {
		return err
	}
	return emit(stdout, *asJSON, res, func(w io.Writer) error {
		fmt.Fprintf(w, "Stopped watching %s. %d target(s) remain, revision %d.\n",
			*target, res.Targets, res.ConfigRevision)
		fmt.Fprintf(w, "The session itself was not touched, and what was observed of it is kept.\n")
		return nil
	})
}

func runWatchUpdate(args []string, stdout io.Writer) error {
	fs := flags("watch update")
	id := fs.String("instance", "", "instance id")
	key := fs.String("idempotency-key", "", "retry-safe key: repeating it returns the original result")
	target := fs.String("target", "", "target id to adjust")
	file := fs.String("file", "", "JSON file of policy changes")
	// The Definition of Done's own example — "expect this worker to be quiet
	// for twenty minutes while its tests run" — as one command.
	expectQuiet := fs.Duration("expect-quiet-for", 0, "suppress quiet and idle alerts for this long (e.g. 20m)")
	reason := fs.String("reason", "", "why quiet is expected, recorded alongside the override")
	clearQuiet := fs.Bool("clear-expected-quiet", false, "end an expected-quiet override early")
	quietAfter := fs.Duration("quiet-after", 0, "how long without observed activity before a quiet alert")
	idleGrace := fs.Duration("idle-grace", 0, "how long idle must persist before an idle alert")
	deadline := fs.String("deadline", "", "when the target should have finished: a duration from now (45m) or an RFC3339 time; past it, an unexited target raises one overdue alert")
	clearDeadline := fs.Bool("clear-deadline", false, "remove the target's deadline")
	asJSON := fs.Bool("json", false, "emit JSON for programmatic use")
	if err := parse(fs, args); err != nil {
		return err
	}
	if *target == "" {
		return fmt.Errorf("--target is required")
	}
	if *deadline != "" && *clearDeadline {
		return fmt.Errorf("--deadline and --clear-deadline are alternatives; give one")
	}
	var deadlineAt string
	if *deadline != "" {
		at, err := parseDeadline(*deadline, time.Now())
		if err != nil {
			return err
		}
		deadlineAt = at.UTC().Format(time.RFC3339)
	}
	socket, err := instanceSocket(*id)
	if err != nil {
		return err
	}

	params := daemon.WatchUpdateParams{TargetID: *target}
	if *file != "" {
		data, err := os.ReadFile(*file)
		if err != nil {
			return fmt.Errorf("read update: %w", err)
		}
		if err := json.Unmarshal(data, &params); err != nil {
			return fmt.Errorf("parse update: %w", err)
		}
		params.TargetID = *target
	}
	if *expectQuiet > 0 {
		until := time.Now().Add(*expectQuiet).UTC().Format(time.RFC3339)
		params.ExpectedQuietUntil = &until
		params.ExpectedQuietReason = *reason
	}
	if *clearQuiet {
		empty := ""
		params.ExpectedQuietUntil = &empty
	}
	if *quietAfter > 0 {
		seconds := int(quietAfter.Seconds())
		params.QuietAfterSeconds = &seconds
	}
	if *idleGrace > 0 {
		seconds := int(idleGrace.Seconds())
		params.IdleGraceSeconds = &seconds
	}

	if *deadline != "" {
		params.Deadline = &deadlineAt
	}
	if *clearDeadline {
		empty := ""
		params.Deadline = &empty
	}

	params.IdempotencyKey = *key

	var res daemon.WatchResult
	if err := ipc.Call(socket, "watch.update", params, &res); err != nil {
		return err
	}
	return emit(stdout, *asJSON, res, func(w io.Writer) error {
		fmt.Fprintf(w, "Updated %s, revision %d.\n", *target, res.ConfigRevision)
		if *expectQuiet > 0 {
			fmt.Fprintf(w, "Quiet and idle alerts are suppressed for %s.\n", *expectQuiet)
			fmt.Fprintf(w, "Exit, input requests and observation loss still surface, and observation continues.\n")
		}
		if *deadline != "" {
			fmt.Fprintf(w, "Deadline %s: if %s has not exited by then, one overdue alert is raised.\n", deadlineAt, *target)
		}
		if *clearDeadline {
			fmt.Fprintf(w, "Deadline cleared; an open overdue incident resolves on the next pass.\n")
		}
		fmt.Fprintf(w, "Thresholds apply from now on; what was already observed is unchanged.\n")
		return nil
	})
}

// parseDeadline reads --deadline: a positive duration from now, or an RFC3339
// time. A duration is turned into a time here, as --expect-quiet-for is, so
// what the instance holds is the moment rather than an interval whose start
// would depend on when the request arrived.
func parseDeadline(v string, now time.Time) (time.Time, error) {
	if d, err := time.ParseDuration(v); err == nil {
		if d <= 0 {
			return time.Time{}, fmt.Errorf("--deadline duration must be positive")
		}
		return now.Add(d), nil
	}
	at, err := time.Parse(time.RFC3339, v)
	if err != nil {
		return time.Time{}, fmt.Errorf("--deadline %q is neither a duration (45m) nor an RFC3339 time", v)
	}
	return at, nil
}

func runWatchSync(args []string, stdout io.Writer) error {
	fs := flags("watch sync")
	id := fs.String("instance", "", "instance id")
	key := fs.String("idempotency-key", "", "retry-safe key: repeating it returns the original result")
	file := fs.String("file", "", "configuration to match (default: the file the instance was started with)")
	dryRun := fs.Bool("dry-run", false, "show what would change without changing it")
	asJSON := fs.Bool("json", false, "emit JSON for programmatic use")
	if err := parse(fs, args); err != nil {
		return err
	}
	socket, err := instanceSocket(*id)
	if err != nil {
		return err
	}

	params := daemon.WatchSyncParams{DryRun: *dryRun, IdempotencyKey: *key}
	if *file != "" {
		data, err := os.ReadFile(*file)
		if err != nil {
			return fmt.Errorf("read configuration: %w", err)
		}
		// Refused here as well as by the instance, so a bad file is named
		// before anything is sent.
		if _, err := config.Parse(data); err != nil {
			return err
		}
		params.Config = data
	}

	var res daemon.WatchSyncResult
	if err := ipc.Call(socket, "watch.sync", params, &res); err != nil {
		return err
	}
	return emit(stdout, *asJSON, res, func(w io.Writer) error {
		return renderSync(w, res)
	})
}

func renderSync(w io.Writer, res daemon.WatchSyncResult) error {
	switch {
	case !res.Changed && res.DryRun:
		fmt.Fprintf(w, "Dry run: %s matches what is watched. Nothing would change.\n", res.Source)
	case !res.Changed:
		fmt.Fprintf(w, "%s matches what is watched. Nothing changed; revision %d.\n", res.Source, res.ConfigRevision)
	case res.DryRun:
		fmt.Fprintf(w, "Dry run against %s. Nothing was changed; this is what would be:\n", res.Source)
	default:
		fmt.Fprintf(w, "Synced from %s. %d target(s), revision %d.\n", res.Source, res.Targets, res.ConfigRevision)
	}
	for _, line := range []struct {
		verb string
		ids  []string
	}{{"added", res.Added}, {"removed", res.Removed}, {"updated", res.Updated}, {"unchanged", res.Unchanged}} {
		for _, id := range line.ids {
			fmt.Fprintf(w, "  %-9s %s\n", line.verb, id)
		}
	}
	if res.RetentionChanged {
		fmt.Fprintf(w, "  retention changed to the file's\n")
	}
	if res.FollowFileChanged {
		fmt.Fprintf(w, "  follow_file changed to the file's\n")
	}
	for _, n := range res.NotApplied {
		fmt.Fprintf(w, "  not applied: %s\n", n)
	}
	if len(res.Removed) > 0 && !res.DryRun {
		fmt.Fprintf(w, "Removed sessions were not touched, and what was observed of them is kept.\n")
	}
	return nil
}
