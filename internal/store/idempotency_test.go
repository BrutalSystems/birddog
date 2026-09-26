package store

import (
	"path/filepath"
	"testing"
)

func TestFirstUseOfAKeyHasNoPriorResult(t *testing.T) {
	s := openTemp(t)

	_, ok, err := s.PriorResult("key-1")
	if err != nil {
		t.Fatalf("PriorResult: %v", err)
	}
	if ok {
		t.Error("ok = true for a key never seen")
	}
}

// A retried mutation must return what the first one did, not apply again. A
// caller that did not hear the answer will retry, and adding a watch twice or
// clearing an override twice is not what they asked for.
func TestARepeatedKeyReturnsTheOriginalResult(t *testing.T) {
	s := openTemp(t)
	if err := s.RememberResult("key-1", []byte(`{"config_revision":4}`)); err != nil {
		t.Fatalf("RememberResult: %v", err)
	}

	got, ok, err := s.PriorResult("key-1")
	if err != nil || !ok {
		t.Fatalf("PriorResult: %v ok=%v", err, ok)
	}
	if string(got) != `{"config_revision":4}` {
		t.Errorf("result = %s, want the original", got)
	}
}

func TestDifferentKeysAreIndependent(t *testing.T) {
	s := openTemp(t)
	_ = s.RememberResult("key-1", []byte(`{"a":1}`))

	if _, ok, _ := s.PriorResult("key-2"); ok {
		t.Error("ok = true for a different key")
	}
}

// Remembering the same key twice keeps the first answer: the original result
// is the one the caller is entitled to see.
func TestRememberingAKeyTwiceKeepsTheFirstResult(t *testing.T) {
	s := openTemp(t)
	_ = s.RememberResult("key-1", []byte(`{"first":true}`))
	_ = s.RememberResult("key-1", []byte(`{"second":true}`))

	got, _, _ := s.PriorResult("key-1")
	if string(got) != `{"first":true}` {
		t.Errorf("result = %s, want the first", got)
	}
}

// A retry after a restart is the case the key exists for.
func TestRememberedResultsSurviveRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "i.db")

	s, _ := Open(path)
	_ = s.RememberResult("key-1", []byte(`{"config_revision":7}`))
	s.Close()

	reopened, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer reopened.Close()

	got, ok, err := reopened.PriorResult("key-1")
	if err != nil || !ok {
		t.Fatalf("PriorResult after restart: %v ok=%v", err, ok)
	}
	if string(got) != `{"config_revision":7}` {
		t.Errorf("result = %s", got)
	}
}
