package observe

import "strings"

import "testing"

// The writer bounds the subject it records. The reader must bound it too: the
// two ship together today, but a plugin specifier is resolved once into a
// package cache and never revisited, so an older plugin can keep writing
// records long after a newer one was published. A guarantee enforced only on
// the far side of a boundary that is known to drift is not a guarantee.
func TestAnOverlongDetailIsBoundedOnRead(t *testing.T) {
	got := boundDetail(strings.Repeat("a", maxDetail+50))

	if len([]rune(got)) != maxDetail+1 {
		t.Fatalf("bounded length = %d runes, want %d plus the marker", len([]rune(got)), maxDetail)
	}
	if !strings.HasSuffix(got, "…") {
		t.Errorf("bounded detail = %q, want it to end with the truncation marker", got)
	}
}

// Truncation must not be silent: a value cut short and one that happened to be
// exactly that long are different facts.
func TestABoundedDetailSaysItWasCut(t *testing.T) {
	exact := strings.Repeat("a", maxDetail)

	if got := boundDetail(exact); got != exact {
		t.Errorf("a detail at exactly the bound was altered: %q", got)
	}
	if got := boundDetail(exact + "a"); !strings.HasSuffix(got, "…") {
		t.Errorf("a detail one over the bound = %q, want the truncation marker", got)
	}
}

// Truncating by bytes would split a multi-byte character and put an invalid
// rune into the durable record.
func TestBoundingDoesNotSplitAMultiByteCharacter(t *testing.T) {
	got := boundDetail(strings.Repeat("é", maxDetail+10))

	for _, r := range got {
		if r == '�' {
			t.Fatalf("bounded detail contains a replacement rune: %q", got)
		}
	}
}

// An over-long value is a malformed record, not a reason to discard the
// observation. birddog reports what it saw, bounded.
func TestAShortDetailIsUntouched(t *testing.T) {
	for _, in := range []string{"", "bash", "bash: rm test.tst"} {
		if got := boundDetail(in); got != in {
			t.Errorf("boundDetail(%q) = %q, want it unchanged", in, got)
		}
	}
}
