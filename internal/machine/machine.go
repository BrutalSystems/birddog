// Package machine carries the identity of the machine birddog is observing
// from.
//
// birddog does not mint this. Machine identity belongs to the layer that owns
// connectivity and trust, and a hostname is not an identity — it is neither
// stable nor unique, and a replacement standing in the same place reuses it.
// This package accepts one, checks it is usable, and compares two of them.
//
// Unset is not an error. It means local mode: one machine, no qualification
// needed, and exactly the behaviour birddog had before identities existed.
package machine

import (
	"errors"
	"fmt"
	"os"
	"unicode"
	"unicode/utf8"
)

// EnvVar is where a supervising layer supplies the identity.
const EnvVar = "BIRDDOG_MACHINE"

// MaxLen bounds the identity in bytes.
//
// Not a statement about what an identity looks like — only that it must not be
// unreasonable enough to damage a record or a log line. Larger than the 24
// that bounds an instance id, because that bound exists to keep a unix socket
// path under a platform limit and a machine identity goes into no path.
const MaxLen = 128

// ErrMachineMismatch reports a position that cannot be honoured here, because
// it belongs to a different machine or to one this daemon cannot confirm it is.
//
// The sibling of store.ErrStoreReplaced, one level out: that one says the
// history was replaced, this says the history is somebody else's.
var ErrMachineMismatch = errors.New("position belongs to a different machine")

// FromEnv reads and validates the supplied identity.
func FromEnv() (string, error) { return Validate(os.Getenv(EnvVar)) }

// Validate reports whether v can be used as an identity, returning it
// unchanged when it can.
//
// Unchanged is the point. Trimming padding would mean birddog supplying an
// identity subtly different from the one it was handed, which is the same
// class of mistake as inventing one outright.
func Validate(v string) (string, error) {
	if v == "" {
		return "", nil // local mode
	}
	if len(v) > MaxLen {
		return "", fmt.Errorf("%s is %d bytes, at most %d are allowed", EnvVar, len(v), MaxLen)
	}
	// Before the rune loop, which would decode every invalid byte to U+FFFD
	// and find it printable. Accepting one is not cosmetic: encoding/json
	// substitutes U+FFFD on the way out, so the daemon would advertise an
	// identity not byte-equal to the one it holds and could never accept it
	// back — and two different malformed values would collapse to the same
	// wire string, which is the confusion this exists to remove.
	if !utf8.ValidString(v) {
		return "", fmt.Errorf("%s is not valid UTF-8: %q", EnvVar, v)
	}
	for _, r := range v {
		if unicode.IsSpace(r) {
			return "", fmt.Errorf("%s contains whitespace, which is refused rather than trimmed: %q", EnvVar, v)
		}
		if !unicode.IsPrint(r) {
			return "", fmt.Errorf("%s contains a non-printable character: %q", EnvVar, v)
		}
	}
	return v, nil
}

// Check reports whether a position claiming to come from machine `claimed` can
// be honoured by a daemon configured as `configured`.
//
// An empty claim asks nothing and is always honoured: a caller that carries no
// identity gets the behaviour it had before there were any.
//
// A claim this daemon cannot confirm is refused, not accepted. Accepting it
// would let a caller believe its position was qualified when nothing qualified
// it, and a silently misattributed position is the failure this exists to
// prevent.
func Check(claimed, configured string) error {
	switch {
	case claimed == "":
		return nil
	case claimed == configured:
		return nil
	case configured == "":
		return fmt.Errorf("position is from machine %s; this daemon has no machine identity, so it cannot confirm it is %s (set %s): %w",
			claimed, claimed, EnvVar, ErrMachineMismatch)
	default:
		return fmt.Errorf("position is from machine %s, this is machine %s: %w", claimed, configured, ErrMachineMismatch)
	}
}
