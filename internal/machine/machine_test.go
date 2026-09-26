package machine

import (
	"errors"
	"strings"
	"testing"
)

// An identity birddog is handed is used as given. Trimming padding would
// invent a second identity that nobody supplied.
func TestValidateRejectsRatherThanTrims(t *testing.T) {
	for _, v := range []string{" ferry", "ferry ", "fer ry", "ferry\t", "ferry\n"} {
		if _, err := Validate(v); err == nil {
			t.Errorf("Validate(%q) = nil, want an error: whitespace must be refused, not trimmed", v)
		}
	}
}

func TestValidateRejectsNonPrintable(t *testing.T) {
	if _, err := Validate("ferry\x00id"); err == nil {
		t.Error("Validate with a NUL = nil, want an error")
	}
}

// Exactly at the bound is legitimate; one past it is not.
func TestValidateBoundIsExact(t *testing.T) {
	at := strings.Repeat("a", MaxLen)
	if _, err := Validate(at); err != nil {
		t.Errorf("Validate(%d chars) = %v, want nil", MaxLen, err)
	}
	over := strings.Repeat("a", MaxLen+1)
	if _, err := Validate(over); err == nil {
		t.Errorf("Validate(%d chars) = nil, want an error", MaxLen+1)
	}
}

// birddog imposes no scheme. Anything printable and unspaced is somebody
// else's identifier and is accepted as given.
func TestValidateImposesNoFormat(t *testing.T) {
	for _, v := range []string{"ferry:7c3a91b2", "host-01.internal", "urn:uuid:9f8e", "台北-1"} {
		got, err := Validate(v)
		if err != nil {
			t.Errorf("Validate(%q) = %v, want it accepted", v, err)
		}
		if got != v {
			t.Errorf("Validate(%q) = %q, want it returned unchanged", v, got)
		}
	}
}

// Empty is not an error. It is local mode.
func TestValidateAcceptsEmptyAsLocalMode(t *testing.T) {
	got, err := Validate("")
	if err != nil || got != "" {
		t.Errorf("Validate(\"\") = %q, %v; want \"\", nil", got, err)
	}
}

// The acceptance table from the spec, D-5.
func TestCheck(t *testing.T) {
	cases := []struct {
		name                string
		claimed, configured string
		wantErr             bool
	}{
		{"caller carries nothing", "", "A", false},
		{"caller carries nothing, daemon has nothing", "", "", false},
		{"agreement", "A", "A", false},
		{"disagreement", "A", "B", true},
		{"daemon cannot confirm the claim", "A", "", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := Check(c.claimed, c.configured)
			if c.wantErr && !errors.Is(err, ErrMachineMismatch) {
				t.Errorf("Check(%q, %q) = %v, want ErrMachineMismatch", c.claimed, c.configured, err)
			}
			if !c.wantErr && err != nil {
				t.Errorf("Check(%q, %q) = %v, want nil", c.claimed, c.configured, err)
			}
		})
	}
}

// The message has to name both sides: an operator reading it needs to know
// what was claimed and what this daemon is.
func TestCheckMessageNamesBothSides(t *testing.T) {
	err := Check("A", "B")
	if !strings.Contains(err.Error(), "A") || !strings.Contains(err.Error(), "B") {
		t.Errorf("message %q does not name both machines", err.Error())
	}
}

func TestFromEnvReadsTheVariable(t *testing.T) {
	t.Setenv(EnvVar, "ferry:7c3a91b2")
	got, err := FromEnv()
	if err != nil || got != "ferry:7c3a91b2" {
		t.Errorf("FromEnv() = %q, %v; want ferry:7c3a91b2, nil", got, err)
	}
}

// Invalid UTF-8 is not printable text, and accepting it is worse than
// cosmetic: encoding/json substitutes U+FFFD on the way out, so the daemon
// advertises an identity that is not byte-equal to the one it holds and can
// never accept it back. Two different malformed values collapse to the same
// wire string, which is the confusion this feature exists to remove.
func TestValidateRejectsInvalidUTF8(t *testing.T) {
	for _, v := range []string{"ferry\xff", "\x80\x81", "fer\xed\xa0\x80ry"} {
		if _, err := Validate(v); err == nil {
			t.Errorf("Validate(%q) = nil, want an error: invalid UTF-8 is mutated on the wire", v)
		}
	}
}
