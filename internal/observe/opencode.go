package observe

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/BrutalSystems/birddog/internal/config"
	"github.com/BrutalSystems/birddog/internal/monitor"
	"github.com/BrutalSystems/birddog/internal/policy"
	"github.com/BrutalSystems/birddog/internal/provider"
)

// Opencode observes opencode sessions through the records the birddog plugin
// publishes.
//
// A default opencode TUI exposes nothing an outside process can find, so this
// adapter reads what the plugin writes and nothing else. A session launched
// without the plugin is not observable, and says so rather than appearing to
// be quiet.
type Opencode struct {
	// Dir holds one record per live session.
	Dir string

	// ProcessLive reports whether the publishing process is still running.
	ProcessLive func(pid int) bool

	// StaleAfter is how long a record may go unrefreshed before the session
	// behind it is treated as unobservable.
	StaleAfter time.Duration
}

// defaultStaleAfter allows several missed heartbeats before giving up, so a
// busy machine does not look like a dead one.
const defaultStaleAfter = 90 * time.Second

// NewOpencode builds an observer over the plugin's record directory.
func NewOpencode(dir string) *Opencode {
	return &Opencode{Dir: dir, ProcessLive: processLive}
}

// openCodeRecord is what the plugin publishes.
type openCodeRecord struct {
	SessionID       string `json:"session_id"`
	Slug            string `json:"slug"`
	Directory       string `json:"directory"`
	State           string `json:"state"`
	LastActivityAt  string `json:"last_activity_at"`
	UpdatedAt       string `json:"updated_at"`
	PID             int    `json:"pid"`
	PluginVersion   string `json:"plugin_version"`
	OpencodeVersion string `json:"opencode_version"`

	PendingPermission *struct {
		ID      string `json:"id"`
		AskedAt string `json:"asked_at"`
		Type    string `json:"type"`
		Detail  string `json:"detail"`
	} `json:"pending_permission,omitempty"`

	CurrentTool *struct {
		Name      string `json:"name"`
		StartedAt string `json:"started_at"`
	} `json:"current_tool,omitempty"`
}

// openCodeStates is the vocabulary the plugin publishes. Anything outside it
// is a plugin newer than this build, and is reported as unreadable rather
// than guessed at.
var openCodeStates = map[string]bool{
	policy.StatusActive:       true,
	policy.StatusIdle:         true,
	policy.StatusWaitingInput: true,
	policy.StatusRunningTool:  true,
}

// Observe reports the current state of one opencode session.
func (o *Opencode) Observe(target config.Target) (monitor.Sighting, error) {
	path := filepath.Join(o.Dir, target.Attachment.SessionID+".json")

	data, err := os.ReadFile(path)
	if err != nil {
		// Most likely the plugin is not installed, so there is nothing to
		// read rather than a session that ended. Claiming an exit here would
		// report a running session as finished.
		return unobservable(policy.ReasonProviderLimited), fmt.Errorf(
			"no birddog record for opencode session %s: the plugin may not be installed (%w)",
			target.Attachment.SessionID, err)
	}

	var rec openCodeRecord
	if err := json.Unmarshal(data, &rec); err != nil {
		return unobservable(policy.ReasonSourceUnreadable), fmt.Errorf("birddog record %s is unreadable: %w", path, err)
	}

	status, known := rec.State, openCodeStates[rec.State]
	if !known {
		status = policy.StatusUnknown
	}

	sighting := monitor.Sighting{
		Observation: policy.Observation{
			Live:           o.live(rec),
			Status:         status,
			StatusKnown:    known,
			LastActivityAt: parseStamp(rec.LastActivityAt),
			// The plugin restamps at every tool boundary, so staleness here is
			// a real measure of silence.
			ActivityResolution: policy.ResolutionActivity,
			ObservedAt:         time.Now().UTC(),
			// The plugin watches permission.asked and permission.replied, so
			// this is the one provider where an absence really is an
			// observation rather than a blind spot.
			InputRequestVisibility: visibilityOf(rec),
			InputRequestKind:       requestKind(rec),
			InputRequestDetail:     requestSubject(rec),
			Evidence:               evidenceOf(rec),
		},
		// The publishing process is part of the identity: a session
		// republished by another process is a different run.
		SessionIdentity: fmt.Sprintf("%s@%d", rec.SessionID, rec.PID),
	}
	if rec.CurrentTool != nil {
		sighting.StatusSince = parseStamp(rec.CurrentTool.StartedAt)
	} else if rec.PendingPermission != nil {
		sighting.StatusSince = parseStamp(rec.PendingPermission.AskedAt)
	} else {
		sighting.StatusSince = sighting.LastActivityAt
	}

	sighting.Reason = opencodeReason(sighting.Live, known)

	if !sighting.Live {
		return sighting, fmt.Errorf("opencode session %s is no longer being published", rec.SessionID)
	}
	return sighting, nil
}

// live requires both the publishing process and a fresh heartbeat.
//
// Either alone is not enough. A record outlives the process that wrote it, and
// a pid outlives the process that held it — a stale record naming a reused pid
// would otherwise read as a healthy session.
func (o *Opencode) live(rec openCodeRecord) bool {
	stale := o.StaleAfter
	if stale <= 0 {
		stale = defaultStaleAfter
	}

	updated := parseStamp(rec.UpdatedAt)
	if updated.IsZero() || time.Since(updated) > stale {
		return false
	}

	alive := o.ProcessLive
	if alive == nil {
		alive = processLive
	}
	return rec.PID > 0 && alive(rec.PID)
}

// opencodeReason says why an observation is not a current, readable answer.
func opencodeReason(live, statusKnown bool) string {
	switch {
	case !live:
		return policy.ReasonContactLost
	case !statusKnown:
		return policy.ReasonStatusUnrecognised
	default:
		return ""
	}
}

// requestKind is what opencode called the kind of request — "bash", "edit".
//
// Carried verbatim rather than mapped onto a vocabulary of birddog's own: an
// orchestrator branching on "is this a destructive command" needs what the
// provider actually said, and inventing categories would assert an
// equivalence between providers that nobody established.
func requestKind(rec openCodeRecord) string {
	if rec.PendingPermission == nil {
		return ""
	}
	return boundDetail(rec.PendingPermission.Type)
}

// requestSubject is what an observed request is asking to act on, where the
// plugin said. Empty when nothing is outstanding, or when the provider said
// only that something is.
func requestSubject(rec openCodeRecord) string {
	if rec.PendingPermission == nil {
		return ""
	}
	return boundDetail(rec.PendingPermission.Detail)
}

// evidenceOf sources an observed request to the plugin, and is empty when
// nothing is outstanding.
func evidenceOf(rec openCodeRecord) string {
	if rec.PendingPermission == nil {
		return ""
	}
	return policy.EvidenceHook
}

func visibilityOf(rec openCodeRecord) string {
	if rec.PendingPermission != nil {
		return policy.VisibilityObserved
	}
	return policy.VisibilityNotObserved
}

// unobservable is the observation of having seen nothing at all. Visibility is
// unavailable rather than not-observed: birddog could not look.
// unobservable is the observation of having seen nothing.
//
// The reason matters here more than elsewhere: an opencode session is only
// observable if it was launched with the plugin, so "no record" is usually a
// capability limit rather than a fault — the session may be running perfectly
// and simply be invisible from outside.
func unobservable(reason string) monitor.Sighting {
	return monitor.Sighting{Observation: policy.Observation{
		Live: false, Status: policy.StatusUnknown, StatusKnown: false,
		InputRequestVisibility: policy.VisibilityUnavailable,
		ActivityResolution:     policy.ResolutionUnavailable,
		Reason:                 reason,
	}}
}

func parseStamp(v string) time.Time {
	if v == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339, v)
	if err != nil {
		return time.Time{}
	}
	return t.UTC()
}

// processLive reports whether a pid is running, without disturbing it.
func processLive(pid int) bool {
	if pid <= 0 {
		return false
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return p.Signal(syscall.Signal(0)) == nil
}

func init() {
	provider.Register(provider.Spec{
		Name: "opencode", Command: "opencode",
		AttachToRunningSession: "yes — when launched with the birddog plugin; a plain TUI exposes nothing",
		RuntimeState:           "yes — with the plugin: active, idle, running_tool, waiting_input",
		ToolEvents:             "yes — with the plugin, including the running tool's name",
		InputRequests:          "yes — with the plugin, including what the request is asking to act on",
		Notes:                  "install with muster: [plugins.birddog] npm = \"@brutalsystems/birddog-opencode\"",

		// Every one of these is carried by the plugin. A plain TUI exposes
		// nothing, which is a setup gap and not a limit of opencode.
		AttachToRunningSessionSupport: policy.SupportRequiresSetup,
		RuntimeStateSupport:           policy.SupportRequiresSetup,
		ToolEventsSupport:             policy.SupportRequiresSetup,
		InputRequestsSupport:          policy.SupportRequiresSetup,
	})
	register("opencode", func(d Deps) (monitor.Observer, error) {
		return NewOpencode(d.OpencodeRecords), nil
	})
}
