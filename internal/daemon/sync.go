package daemon

import (
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"time"

	"github.com/BrutalSystems/birddog/internal/config"
	"github.com/BrutalSystems/birddog/internal/ipc"
)

// Sync makes an instance watch exactly what a configuration file says, so an
// orchestrator that keeps its watch list as a file — a ledger — can change the
// file and have the instance follow it, rather than translating each edit into
// a watch add or remove by hand.
//
// It is the same change as the add, remove and update it replaces, and held to
// the same rules: the whole file is validated before any of it is applied, and
// one revision covers everything it changed. A target that is unchanged is
// left exactly as it was — its state, its incidents and its history are keyed
// by target id and are not touched by being restated.

// WatchSyncParams asks an instance to match a configuration.
type WatchSyncParams struct {
	// Config is a whole instance configuration, in the file format. Empty
	// means re-read the file the instance was started with.
	Config json.RawMessage `json:"config,omitempty"`

	// DryRun reports what would change and changes nothing.
	DryRun bool `json:"dry_run,omitempty"`

	IdempotencyKey string `json:"idempotency_key,omitempty"`
}

// WatchSyncResult says what a sync changed, target by target.
type WatchSyncResult struct {
	WatchResult

	// Source is the file that was read, or "request" when the configuration
	// came with the call.
	Source string `json:"source"`

	Added     []string `json:"added"`
	Removed   []string `json:"removed"`
	Updated   []string `json:"updated"`
	Unchanged []string `json:"unchanged"`

	// RetentionChanged says the retention window was changed to the file's.
	RetentionChanged bool `json:"retention_changed,omitempty"`

	// FollowFileChanged says follow_file was changed to the file's. It
	// changes how the instance reads its file, not what it watches, so it
	// advances no revision.
	FollowFileChanged bool `json:"follow_file_changed,omitempty"`

	// NotApplied names what differs and was deliberately not applied, and
	// why. The instance's name and where it delivers alerts are fixed for
	// the life of the process; they change when it is resumed.
	NotApplied []string `json:"not_applied,omitempty"`

	Changed bool `json:"changed"`
	DryRun  bool `json:"dry_run,omitempty"`
}

// ConfigSyncView is the state of an instance's own configuration file, as
// status reports it.
type ConfigSyncView struct {
	File string `json:"file,omitempty"`

	// FollowFile says the instance re-reads File whenever it changes.
	FollowFile bool `json:"follow_file"`

	LastSyncedAt *time.Time `json:"last_synced_at,omitempty"`

	// LastError is why the last sync from the file was refused. The watch
	// list is left as it was, so a typo in the file never empties it. Cleared
	// by the next sync that succeeds.
	LastError string `json:"last_error,omitempty"`
}

// fileStamp is enough of a file's identity to notice it has been rewritten.
type fileStamp struct {
	mod  time.Time
	size int64
}

func stampOf(path string) (fileStamp, bool) {
	info, err := os.Stat(path)
	if err != nil {
		return fileStamp{}, false
	}
	return fileStamp{mod: info.ModTime(), size: info.Size()}, true
}

func (d *Daemon) watchSync(params json.RawMessage) (any, error) {
	var p WatchSyncParams
	if len(params) > 0 {
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, &ipc.Error{Code: ipc.CodeBadRequest, Message: err.Error()}
		}
	}
	if !p.DryRun {
		if prior, ok, err := d.priorResult(p.IdempotencyKey); err != nil || ok {
			return prior, err
		}
	}

	data, source := []byte(p.Config), "request"
	if len(data) == 0 {
		if d.opts.ConfigPath == "" {
			return nil, &ipc.Error{Code: ipc.CodeBadRequest, Message: "this instance has no configuration file to re-read; send one"}
		}
		source = d.opts.ConfigPath
		var err error
		if data, err = os.ReadFile(source); err != nil {
			return nil, &ipc.Error{Code: ipc.CodeBadRequest, Message: fmt.Sprintf("read %s: %v", source, err)}
		}
	}

	result, err := d.sync(data, source, p.DryRun)
	// Only a sync from the file says anything about the file. One sent with
	// the request may match a different file entirely.
	if source == d.opts.ConfigPath && !p.DryRun {
		d.noteSync(err)
	}
	if err != nil {
		return nil, &ipc.Error{Code: ipc.CodeBadRequest, Message: err.Error()}
	}
	if p.DryRun {
		return result, nil
	}
	return d.remember(p.IdempotencyKey, result)
}

// sync applies a configuration's watch list, or reports what applying it
// would do. Nothing changes unless all of it is valid.
func (d *Daemon) sync(data []byte, source string, dryRun bool) (WatchSyncResult, error) {
	cfg, err := config.Parse(data)
	if err != nil {
		return WatchSyncResult{}, err
	}

	d.cfgMu.Lock()
	defer d.cfgMu.Unlock()

	now := d.now()
	result := WatchSyncResult{
		Source: source, DryRun: dryRun,
		Added: []string{}, Removed: []string{}, Updated: []string{}, Unchanged: []string{},
	}

	current := map[string]config.Target{}
	for _, t := range d.opts.Config.Targets {
		current[t.ID] = t
	}

	next := make([]config.Target, 0, len(cfg.Targets))
	for _, t := range cfg.Targets {
		cur, ok := current[t.ID]
		if !ok {
			result.Added = append(result.Added, t.ID)
			next = append(next, t)
			continue
		}
		delete(current, t.ID)

		t.Policy = keepRuntimeOverride(t.Policy, cur.Policy, now)
		// A file naming an id the target has since been followed away from
		// means the session being followed, so it is unchanged. The file's
		// own target is what is kept when it differs: the next pass decides
		// whether a recorded follow still applies, as it does at start.
		resolved := t
		d.monitor.ResolveFollows(&resolved)
		if reflect.DeepEqual(cur, t) || reflect.DeepEqual(cur, resolved) {
			result.Unchanged = append(result.Unchanged, t.ID)
			next = append(next, cur)
			continue
		}
		result.Updated = append(result.Updated, t.ID)
		next = append(next, t)
	}
	for _, t := range d.opts.Config.Targets {
		if _, gone := current[t.ID]; gone {
			result.Removed = append(result.Removed, t.ID)
		}
	}

	result.RetentionChanged = !reflect.DeepEqual(cfg.Retention, d.opts.Config.Retention)
	if cfg.Name != d.opts.Config.Name {
		result.NotApplied = append(result.NotApplied, "name: fixed for the life of the instance")
	}
	if !reflect.DeepEqual(cfg.Notification, d.opts.Config.Notification) {
		result.NotApplied = append(result.NotApplied, "notification: takes effect when the instance is resumed")
	}

	result.FollowFileChanged = cfg.FollowFile != d.opts.Config.FollowFile
	if result.FollowFileChanged && !dryRun {
		d.opts.Config.FollowFile = cfg.FollowFile
	}
	watchChanged := len(result.Added)+len(result.Removed)+len(result.Updated) > 0 || result.RetentionChanged
	result.Changed = watchChanged || result.FollowFileChanged
	if dryRun || !watchChanged {
		result.ConfigRevision = d.configRev
		result.Targets = len(d.opts.Config.Targets)
		result.Note = watchNote
		if dryRun {
			result.Note = "dry run: nothing was changed. " + watchNote
		}
		return result, nil
	}

	// Removing a target keeps its events and incidents, as watch remove
	// does: stopping watching does not make what was observed untrue.
	d.opts.Config.Targets = next
	d.opts.Config.Retention = cfg.Retention
	d.opts.Config.FollowFile = cfg.FollowFile
	result.WatchResult = d.bumpRevision()
	return result, nil
}

// keepRuntimeOverride carries an expected-quiet override set at runtime across
// a sync from a file that does not mention one.
//
// The override is the one thing `watch update` sets that a ledger is not
// expected to record — it is for "this worker's tests run for twenty minutes",
// set by whoever knows that, and a sync that silently ended it would raise
// exactly the quiet alerts it was set to suppress. A file that sets an
// override of its own wins; one that has lapsed is not carried.
func keepRuntimeOverride(fromFile, current config.Policy, now time.Time) config.Policy {
	if fromFile.ExpectedQuietUntil != nil || current.ExpectedQuietUntil == nil {
		return fromFile
	}
	if !now.Before(*current.ExpectedQuietUntil) {
		return fromFile
	}
	fromFile.ExpectedQuietUntil = current.ExpectedQuietUntil
	fromFile.ExpectedQuietReason = current.ExpectedQuietReason
	return fromFile
}

// followFile re-reads the instance's configuration file when it has changed,
// for an instance configured with follow_file. Called from the observation
// loop before each pass.
//
// A file that does not parse is refused and recorded, and the watch list is
// left as it was: an editor's half-written save must not empty it. The file is
// not retried until it changes again.
func (d *Daemon) followFile() {
	d.cfgMu.Lock()
	follow := d.opts.Config.FollowFile
	d.cfgMu.Unlock()
	if !follow || d.opts.ConfigPath == "" {
		return
	}

	stamp, ok := stampOf(d.opts.ConfigPath)
	d.syncMu.Lock()
	if !ok || stamp == d.fileStamp {
		d.syncMu.Unlock()
		return
	}
	d.fileStamp = stamp
	d.syncMu.Unlock()

	data, err := os.ReadFile(d.opts.ConfigPath)
	if err == nil {
		_, err = d.sync(data, d.opts.ConfigPath, false)
	}
	d.noteSync(err)
	if err != nil {
		fmt.Fprintf(os.Stderr, "birddog: %s changed and was not applied: %v\n", d.opts.ConfigPath, err)
	}
}

func (d *Daemon) noteSync(err error) {
	d.syncMu.Lock()
	defer d.syncMu.Unlock()
	if err != nil {
		d.syncErr = err.Error()
		return
	}
	at := d.now().UTC()
	d.syncedAt = &at
	d.syncErr = ""
}

func (d *Daemon) configSyncView() *ConfigSyncView {
	d.cfgMu.Lock()
	follow := d.opts.Config.FollowFile
	d.cfgMu.Unlock()

	d.syncMu.Lock()
	defer d.syncMu.Unlock()
	if d.opts.ConfigPath == "" && d.syncedAt == nil && d.syncErr == "" {
		return nil
	}
	return &ConfigSyncView{
		File: d.opts.ConfigPath, FollowFile: follow,
		LastSyncedAt: d.syncedAt, LastError: d.syncErr,
	}
}
