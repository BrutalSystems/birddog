package observe

import (
	"errors"
	"testing"

	"github.com/BrutalSystems/birddog/internal/config"
	"github.com/BrutalSystems/birddog/internal/discover/claude"
	"github.com/BrutalSystems/birddog/internal/policy"
)

// birddog already says it could not see. The reason says which of several
// situations that was, because they call for different responses — and the
// most important distinction is whether asking again could ever help.
func TestReasonsDistinguishWhyAnAnswerIsIndeterminate(t *testing.T) {
	t.Run("a session that cannot be reached", func(t *testing.T) {
		o := &Claude{List: listing(claude.Session{
			SessionID: "s1", Status: "busy", Live: false, StatusUpdatedAt: t0,
		})}
		got, _ := o.Observe(claudeTarget("s1"))
		if got.Reason != policy.ReasonContactLost {
			t.Errorf("Reason = %q, want contact_lost", got.Reason)
		}
	})

	t.Run("a session reporting a value birddog will not interpret", func(t *testing.T) {
		o := &Claude{List: listing(claude.Session{
			SessionID: "s1", Status: "compacting", Live: true, StatusUpdatedAt: t0,
		})}
		got, _ := o.Observe(claudeTarget("s1"))
		if got.Reason != policy.ReasonStatusUnrecognised {
			t.Errorf("Reason = %q, want status_unrecognised", got.Reason)
		}
	})

	t.Run("a registry that cannot be read", func(t *testing.T) {
		o := &Claude{List: func() ([]claude.Session, error) {
			return nil, errors.New("permission denied")
		}}
		got, _ := o.Observe(claudeTarget("s1"))
		if got.Reason != policy.ReasonSourceUnreadable {
			t.Errorf("Reason = %q, want source_unreadable", got.Reason)
		}
	})
}

// A current, readable answer carries no reason. A reason is for the
// indeterminate case, not decoration on every observation.
func TestAReadableObservationCarriesNoReason(t *testing.T) {
	o := &Claude{List: listing(claude.Session{
		SessionID: "s1", Status: "busy", Live: true, StatusUpdatedAt: t0,
	})}

	got, _ := o.Observe(claudeTarget("s1"))
	if got.Reason != "" {
		t.Errorf("Reason = %q, want none for a current readable status", got.Reason)
	}
}

// The distinction that matters most to a consumer: provider_limited will not
// change by asking again, where the others might.
func TestAnOpencodeSessionWithoutThePluginIsAProviderLimit(t *testing.T) {
	o := &Opencode{Dir: t.TempDir()}

	got, _ := o.Observe(config.Target{
		ID: "worker-1", Provider: "opencode",
		Attachment: config.Attachment{Kind: "existing-session", SessionID: "missing"},
	})
	if got.Reason != policy.ReasonProviderLimited {
		t.Errorf("Reason = %q, want provider_limited: an uninstrumented session is invisible, not faulty", got.Reason)
	}
}
