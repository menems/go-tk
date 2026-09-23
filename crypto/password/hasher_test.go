package password_test

import (
	"bytes"
	"errors"
	"testing"

	"github.com/menems/go-tk/crypto/password"
)

// stubHasher is the hasher a consumer writes: its fields are the methods of
// the seam, and each test sets the one its case reaches. Nothing of the
// carrier is available to it beyond the plaintext it is handed, which is the
// property that lets a service implement this seam from its own module.
type stubHasher struct {
	hash   func(plaintext []byte) (string, error)
	verify func(stored string, plaintext []byte) error
}

func (s stubHasher) Hash(plaintext []byte) (string, error) { return s.hash(plaintext) }

func (s stubHasher) Verify(stored string, plaintext []byte) error {
	return s.verify(stored, plaintext)
}

func TestEveryHashGoesThroughTheHasherHandedIn(t *testing.T) {
	t.Parallel()

	var seen []byte
	h := stubHasher{hash: func(plaintext []byte) (string, error) {
		seen = bytes.Clone(plaintext)
		return "the-stored-value", nil
	}}

	got, err := newPassword(t, plaintext).Hash(h)
	if err != nil {
		t.Fatalf("Hash = %v, want the stored value the hasher wrote", err)
	}
	if got != "the-stored-value" {
		t.Errorf("Hash = %q, want the stored value the hasher wrote", got)
	}
	if string(seen) != plaintext {
		t.Errorf("the hasher was handed %q, want the plaintext the carrier holds", seen)
	}
}

func TestAHashTheHasherRefusesIsRefused(t *testing.T) {
	t.Parallel()

	sentinel := errors.New("the hasher's own refusal")
	h := stubHasher{hash: func([]byte) (string, error) { return "", sentinel }}

	got, err := newPassword(t, plaintext).Hash(h)
	if !errors.Is(err, sentinel) {
		t.Fatalf("Hash = %v, want the hasher's own error", err)
	}
	if got != "" {
		t.Errorf("Hash = %q, want no stored value", got)
	}
}

func TestEveryVerificationGoesThroughTheHasherHandedIn(t *testing.T) {
	t.Parallel()

	var seenStored string
	var seenPlaintext []byte
	h := stubHasher{verify: func(stored string, plaintext []byte) error {
		seenStored, seenPlaintext = stored, bytes.Clone(plaintext)
		return nil
	}}

	if err := newPassword(t, plaintext).Verify(h, "the-stored-value"); err != nil {
		t.Fatalf("Verify = %v, want the hasher's own verdict", err)
	}
	if seenStored != "the-stored-value" {
		t.Errorf("the hasher was handed %q, want the stored value", seenStored)
	}
	if string(seenPlaintext) != plaintext {
		t.Errorf("the hasher was handed %q, want the plaintext the carrier holds", seenPlaintext)
	}
}

func TestAVerificationTheHasherRefusesIsRefused(t *testing.T) {
	t.Parallel()

	h := stubHasher{verify: func(string, []byte) error { return password.ErrMismatch }}

	if err := newPassword(t, plaintext).Verify(h, "the-stored-value"); !errors.Is(err, password.ErrMismatch) {
		t.Fatalf("Verify = %v, want the hasher's own refusal", err)
	}
}

func TestZeroCarrierIsRefusedRatherThanHashed(t *testing.T) {
	t.Parallel()

	reached := false
	h := stubHasher{
		hash:   func([]byte) (string, error) { reached = true; return "reached", nil },
		verify: func(string, []byte) error { reached = true; return nil },
	}

	var zero password.Password

	t.Run("hashing", func(t *testing.T) {
		got, err := zero.Hash(h)
		if !errors.Is(err, password.ErrEmpty) {
			t.Fatalf("Hash = %v, want ErrEmpty", err)
		}
		if got != "" {
			t.Errorf("Hash = %q, want no stored value", got)
		}
	})

	t.Run("verifying", func(t *testing.T) {
		if err := zero.Verify(h, "the-stored-value"); !errors.Is(err, password.ErrEmpty) {
			t.Fatalf("Verify = %v, want ErrEmpty", err)
		}
	})

	if reached {
		t.Error("the hasher was reached with a carrier holding nothing")
	}
}
