// Package notify delivers attention alerts to an orchestrator.
//
// The recipient is the orchestrator, never a watched worker. Nothing here can
// reach one: an Alert describes what was observed and carries no route back to
// the session it is about.
//
// Delivery is best-effort by design. Every alert stays available through the
// event feed and `status` whatever happens here, so a transport that is down,
// slow or uncertain costs visibility through one channel and nothing else.
package notify

import (
	"fmt"
	"strings"
	"time"
)

// Outcome is what a delivery attempt established.
type Outcome int

const (
	// Delivered: the destination accepted it.
	Delivered Outcome = iota

	// Failed: it definitely did not arrive. Bounded retries are reasonable.
	Failed

	// Unknown: the call returned without establishing anything. An
	// asynchronous API returning promptly is not an acknowledgement. Resending
	// risks duplicates the destination cannot deduplicate, and switching
	// transport risks the same alert arriving twice by different routes — so
	// neither is done. The event feed is the recovery path.
	Unknown

	// Suppressed: no transport is configured, so nothing was attempted.
	Suppressed

	// NoRecipient: a transport is configured, but there is nobody to deliver
	// to — the named session is gone, or birddog cannot authenticate to it.
	//
	// Distinct from Failed, which is a delivery that definitely did not
	// arrive. Nothing was attempted here, and the next attempt would resolve
	// identically, so retrying spends the budget on a configuration error and
	// then reports it as a transport fault. The operator is sent to look at
	// the network instead of at the configuration.
	NoRecipient
)

func (o Outcome) String() string {
	switch o {
	case Delivered:
		return "delivered"
	case Failed:
		return "failed"
	case Unknown:
		return "delivery_unknown"
	case Suppressed:
		return "suppressed"
	case NoRecipient:
		return "no_recipient"
	default:
		return fmt.Sprintf("outcome(%d)", int(o))
	}
}

// Alert is one attention condition, described for a human or an orchestrator.
type Alert struct {
	InstanceID string
	TargetID   string
	IncidentID int64
	Condition  string
	OpenedAt   time.Time

	// Status and Source are what was observed and what saw it.
	Status string
	Source string

	// ObservedAt and StatusIsCurrent make the freshness of the claim explicit.
	ObservedAt      time.Time
	StatusIsCurrent bool

	// InputRequestVisibility is carried so a reader is never left to assume
	// that no observed request means no request.
	InputRequestVisibility string

	// Labels are opaque correlation metadata, passed through untouched.
	Labels map[string]string
}

// Text renders an alert.
//
// It states what was observed, what saw it, and how fresh it is — and stops
// there. "Idle was observed" is a fact; "the worker stalled" is a diagnosis,
// and making it is the orchestrator's business, not birddog's.
func (a Alert) Text() string {
	var b strings.Builder

	fmt.Fprintf(&b, "[%s] %s: %s observed.\n", a.InstanceID, a.TargetID, a.Condition)
	if a.Status != "" {
		freshness := "last known"
		if a.StatusIsCurrent {
			freshness = "current"
		}
		fmt.Fprintf(&b, "State: %s (%s).\n", a.Status, freshness)
	}
	if a.Source != "" {
		fmt.Fprintf(&b, "Source: %s.\n", a.Source)
	}
	if !a.ObservedAt.IsZero() {
		fmt.Fprintf(&b, "Last observation: %s.\n", a.ObservedAt.UTC().Format(time.RFC3339))
	}
	if a.InputRequestVisibility != "" {
		fmt.Fprintf(&b, "Input request: %s.\n", describeVisibility(a.InputRequestVisibility))
	}
	if len(a.Labels) > 0 {
		fmt.Fprintf(&b, "Labels: %s.\n", formatLabels(a.Labels))
	}
	fmt.Fprintf(&b, "Incident %d. Acknowledging records that you saw it; it changes nothing.\n", a.IncidentID)
	return b.String()
}

// describeVisibility spells out what an absence does and does not mean.
func describeVisibility(v string) string {
	switch v {
	case "observed":
		return "observed"
	case "not_observed":
		return "not observed; coverage is limited, so this is not proof there is none"
	default:
		return "cannot be observed for this provider; that is not evidence there is none"
	}
}

func formatLabels(labels map[string]string) string {
	parts := make([]string, 0, len(labels))
	for k, v := range labels {
		parts = append(parts, k+"="+v)
	}
	// Sorted so the same alert reads the same way twice.
	for i := range parts {
		for j := i + 1; j < len(parts); j++ {
			if parts[j] < parts[i] {
				parts[i], parts[j] = parts[j], parts[i]
			}
		}
	}
	return strings.Join(parts, ", ")
}

// Notifier delivers alerts to an orchestrator.
//
// There is no method for reaching a watched worker, and no Alert carries a
// route to one.
type Notifier interface {
	Deliver(Alert) (Outcome, error)
	Name() string
}

// None is the default: alerts stay in the event feed to be polled.
type None struct{}

func (None) Deliver(Alert) (Outcome, error) { return Suppressed, nil }
func (None) Name() string                   { return "none" }

// Fake records deliveries for tests, and can be told how to behave.
type Fake struct {
	Outcome   Outcome
	Err       error
	Delivered []Alert
}

func (f *Fake) Deliver(a Alert) (Outcome, error) {
	f.Delivered = append(f.Delivered, a)
	return f.Outcome, f.Err
}

func (f *Fake) Name() string { return "fake" }
