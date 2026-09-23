package password_test

import (
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/menems/go-tk/crypto/password"
)

// outcome is how the test lets one held call go.
type outcome int

const (
	served outcome = iota
	refused
	panicked
)

var errHeld = errors.New("the held call's own refusal")

// heldHasher is a hasher a test writes whose every call blocks until the test
// lets one go, so a test decides how many places are held and when one frees.
type heldHasher struct {
	calls   atomic.Int32
	entered chan struct{}
	release chan outcome
}

func newHeldHasher() *heldHasher {
	return &heldHasher{entered: make(chan struct{}, 16), release: make(chan outcome)}
}

func (h *heldHasher) hold() error {
	h.calls.Add(1)
	h.entered <- struct{}{}
	switch <-h.release {
	case served:
		return nil
	case refused:
		return errHeld
	default:
		panic("the held call panicked")
	}
}

func (h *heldHasher) Hash([]byte) (string, error) {
	if err := h.hold(); err != nil {
		return "", err
	}
	return "the-held-stored-value", nil
}

func (h *heldHasher) Verify(string, []byte) error { return h.hold() }

// callIn makes one call through h in a goroutine of its own, and signals done
// once it has returned, a panic included: the place it held is back by then.
func callIn(h password.Hasher, verify bool, done chan<- struct{}) {
	go func() {
		defer func() {
			_ = recover()
			done <- struct{}{}
		}()
		if verify {
			_ = h.Verify("the-stored-value", []byte(plaintext))
			return
		}
		_, _ = h.Hash([]byte(plaintext))
	}()
}

func newBound(t *testing.T, h password.Hasher, places int) password.Hasher {
	t.Helper()

	b, err := password.Bound(h, places)
	if err != nil {
		t.Fatalf("Bound = %v, want a hasher", err)
	}
	return b
}

// holdEvery fills the places of a bound over held, and waits until each call
// has reached it.
func holdEvery(t *testing.T, held *heldHasher, b password.Hasher, places int, done chan<- struct{}) {
	t.Helper()

	for i := range places {
		callIn(b, i%2 == 1, done)
	}
	for range places {
		<-held.entered
	}
	if got := held.calls.Load(); got != int32(places) {
		t.Fatalf("the hasher saw %d calls, want the %d holding every place", got, places)
	}
}

func assertBusy(t *testing.T, what string, err error) {
	t.Helper()

	if !errors.Is(err, password.ErrBusy) {
		t.Errorf("%s = %v, want ErrBusy", what, err)
	}
	if errors.Is(err, password.ErrMismatch) || errors.Is(err, password.ErrUnreadable) {
		t.Errorf("%s = %v, want neither ErrMismatch nor ErrUnreadable", what, err)
	}
}

func TestACallIsRefusedAtOnceWhenEveryPlaceIsHeld(t *testing.T) {
	t.Parallel()

	const places = 3

	for name, freed := range map[string]outcome{
		"a held call returning a value":  served,
		"a held call returning an error": refused,
		"a held call panicking":          panicked,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			held := newHeldHasher()
			b := newBound(t, held, places)
			done := make(chan struct{}, 16)
			holdEvery(t, held, b, places, done)

			_, err := b.Hash([]byte(plaintext))
			assertBusy(t, "Hash", err)
			assertBusy(t, "Verify", b.Verify("the-stored-value", []byte(plaintext)))
			if got := held.calls.Load(); got != places {
				t.Fatalf("the hasher saw %d calls, want no call past the %d holding every place", got, places)
			}

			held.release <- freed
			<-done

			callIn(b, false, done)
			<-held.entered
			if got := held.calls.Load(); got != places+1 {
				t.Errorf("the hasher saw %d calls, want the next one to have reached it once a place freed", got)
			}

			for range places {
				held.release <- served
			}
			for range places {
				<-done
			}
		})
	}
}

func TestTheCarrierIsRefusedTheSameWayWhenEveryPlaceIsHeld(t *testing.T) {
	t.Parallel()

	held := newHeldHasher()
	b := newBound(t, held, 1)
	done := make(chan struct{}, 16)
	holdEvery(t, held, b, 1, done)

	var texts []string
	for _, tt := range []struct{ plaintext, stored string }{
		{plaintext, "the-stored-value"},
		{"another-secret", "$pbkdf2-sha256$i=600000$c2FsdA$a2V5"},
		{"a third one", ""},
	} {
		pw := newPassword(t, tt.plaintext)

		_, err := pw.Hash(b)
		assertBusy(t, "Password.Hash", err)
		texts = append(texts, err.Error())

		err = pw.Verify(b, tt.stored)
		assertBusy(t, "Password.Verify", err)
		texts = append(texts, err.Error())

		for _, text := range texts[len(texts)-2:] {
			if strings.Contains(text, tt.plaintext) || (tt.stored != "" && strings.Contains(text, tt.stored)) {
				t.Errorf("the busy refusal %q carries the password or the stored value", text)
			}
		}
	}
	for _, text := range texts {
		if text != password.ErrBusy.Error() {
			t.Errorf("the busy refusal reads %q, want the one fixed %q whatever it was asked", text, password.ErrBusy.Error())
		}
	}
	if got := held.calls.Load(); got != 1 {
		t.Errorf("the hasher saw %d calls, want only the one holding the place", got)
	}

	held.release <- served
	<-done
}

func TestMoreCallsThanPlacesMadeInTurnAreAllServed(t *testing.T) {
	t.Parallel()

	calls := 0
	b := newBound(t, stubHasher{
		hash:   func([]byte) (string, error) { calls++; return "the-stored-value", nil },
		verify: func(string, []byte) error { calls++; return nil },
	}, 2)

	pw := newPassword(t, plaintext)
	for range 3 {
		if _, err := pw.Hash(b); err != nil {
			t.Fatalf("Hash = %v, want it served", err)
		}
		if err := pw.Verify(b, "the-stored-value"); err != nil {
			t.Fatalf("Verify = %v, want it served", err)
		}
	}
	if calls != 6 {
		t.Errorf("the hasher saw %d calls, want all 6", calls)
	}
}

func TestAValueHashedThroughTheBoundVerifiesThroughIt(t *testing.T) {
	t.Parallel()

	b := newBound(t, newHasher(t, password.WithCost(testCost)), 2)
	stored := hashed(t, b, plaintext)

	if err := newPassword(t, plaintext).Verify(b, stored); err != nil {
		t.Errorf("Verify against its own password = %v, want it to go through", err)
	}
	if err := newPassword(t, "another-secret").Verify(b, stored); !errors.Is(err, password.ErrMismatch) {
		t.Errorf("Verify against another password = %v, want ErrMismatch", err)
	}
}

func TestABoundWithNoPlaceOrNoHasherIsRefusedAtWiring(t *testing.T) {
	t.Parallel()

	inner := newHasher(t, password.WithCost(testCost))

	for _, tt := range []struct {
		name   string
		hasher password.Hasher
		places int
		names  string
	}{
		{"no place", inner, 0, "0"},
		{"fewer than none", inner, -3, "-3"},
		{"no hasher", nil, 2, "nil hasher"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			b, err := password.Bound(tt.hasher, tt.places)
			if err == nil {
				t.Fatal("Bound = nil, want a refusal")
			}
			if !strings.Contains(err.Error(), tt.names) {
				t.Errorf("Bound = %q, want it to name the value %q", err, tt.names)
			}
			if b != nil {
				t.Errorf("Bound = %v, want no hasher", b)
			}
		})
	}
}
