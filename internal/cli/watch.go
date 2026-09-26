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
		return fmt.Errorf("watch needs a subcommand: add, remove or update")
	}
	switch args[0] {
	case "add":
		return runWatchAdd(args[1:], stdout)
	case "remove":
		return runWatchRemove(args[1:], stdout)
	case "update":
		return runWatchUpdate(args[1:], stdout)
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
	var target config.Target
	if err := json.Unmarshal(data, &target); err != nil {
		return fmt.Errorf("parse target: %w", err)
	}

	var res daemon.WatchResult
	if err := ipc.Call(socket, "watch.add", daemon.WatchAddParams{Target: target, IdempotencyKey: *key}, &res); err != nil {
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
		fmt.Fprintf(w, "Thresholds apply from now on; what was already observed is unchanged.\n")
		return nil
	})
}
