package cli

import (
	"fmt"
	"io"
	"path/filepath"

	"github.com/BrutalSystems/birddog/internal/instance"
	"github.com/BrutalSystems/birddog/internal/platform/paths"
)

// An instance outlives the session that started it, so records accumulate:
// one per instance ever run, long after the daemons behind them are gone.
// prune clears the ones that are over.
//
// It never stops a running instance. Deciding on its own that a live watch is
// unwanted is the one thing it must not do — an instance is still watching
// because somebody asked it to, and nothing here knows whether they still
// care. `birddog stop` remains the only way to end one.

// PruneEntry is one instance prune would forget, and why.
type PruneEntry struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Reason string `json:"reason"`

	// RemovesHistory is always false: the database outlives the record
	// deliberately, because a stopped instance can be resumed.
	RemovesHistory bool `json:"removes_history"`
}

// PrunePlan is what prune would do, so --dry-run and the real run agree by
// construction rather than by being written twice.
type PrunePlan struct {
	Remove []PruneEntry `json:"remove"`
	Kept   int          `json:"kept"`
}

func planPrune(records []instance.Record) PrunePlan {
	plan := PrunePlan{}
	for _, r := range records {
		switch {
		case r.Running:
			plan.Kept++
		case r.StoppedAt != nil:
			plan.Remove = append(plan.Remove, PruneEntry{
				ID: r.ID, Name: r.Name, Reason: "stopped",
			})
		default:
			// Never marked stopped, and its process is gone: it did not shut
			// down cleanly.
			plan.Remove = append(plan.Remove, PruneEntry{
				ID: r.ID, Name: r.Name, Reason: "process gone without stopping",
			})
		}
	}
	return plan
}

func runPrune(args []string, stdout io.Writer) error {
	fs := flags("prune")
	dryRun := fs.Bool("dry-run", false, "show what would be forgotten without removing anything")
	asJSON := fs.Bool("json", false, "emit JSON for programmatic use")
	if err := parse(fs, args); err != nil {
		return err
	}

	stateDir, err := paths.StateDir()
	if err != nil {
		return err
	}
	registryDir := filepath.Join(stateDir, "registry")

	records, err := instance.ListWithLiveness(registryDir, instance.ProcessAlive)
	if err != nil {
		return err
	}
	plan := planPrune(records)

	if !*dryRun {
		for _, entry := range plan.Remove {
			if err := instance.Deregister(registryDir, entry.ID); err != nil {
				return err
			}
		}
	}

	return emit(stdout, *asJSON, plan, func(w io.Writer) error {
		if len(plan.Remove) == 0 {
			fmt.Fprintf(w, "Nothing to prune. %d instance(s) still running.\n", plan.Kept)
			return nil
		}
		verb := "Forgot"
		if *dryRun {
			verb = "Would forget"
		}
		fmt.Fprintf(w, "%s %d instance(s):\n", verb, len(plan.Remove))
		for _, e := range plan.Remove {
			fmt.Fprintf(w, "  %s  %s  (%s)\n", e.ID, e.Name, e.Reason)
		}
		fmt.Fprintf(w, "\n%d still running, left alone.\n", plan.Kept)
		fmt.Fprintf(w, "Recorded history is kept: a stopped instance can still be resumed.\n")
		return nil
	})
}
