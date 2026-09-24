package sortid_test

import (
	"encoding/binary"
	"errors"
	"regexp"
	"sync"
	"testing"
	"time"
	"uuid"

	"github.com/menems/go-tk/sortid"
)

// start is the instant every test clock begins at, 2026-09-24T10:00:00Z.
var start = time.Date(2026, time.September, 24, 10, 0, 0, 0, time.UTC)

// knownText is a well-formed version 7, RFC 9562 variant id, the root every
// refused sibling below is a variation of.
const knownText = "0192258c-4a7e-7b3c-9d1e-2f3a4b5c6d7e"

// malformed is the one text every refusal carries, whatever arrived.
const malformed = "sortid: not an id this package mints"

// v7Text is the text of a version 7, RFC 9562 variant id, lowercase 8-4-4-4-12.
var v7Text = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

func minter(t *testing.T, now func() time.Time) *sortid.Minter {
	t.Helper()

	m, err := sortid.NewMinter(now)
	if err != nil {
		t.Fatalf("NewMinter = %v, want a minter", err)
	}
	return m
}

func stalled() time.Time { return start }

func TestIDsMintedUnderAStalledClockIncreaseAndKeepIncreasingWhenItStepsBack(t *testing.T) {
	t.Parallel()

	now := start
	m := minter(t, func() time.Time { return now })

	prev := m.Mint()
	for i := range 10_000 - 1 {
		id := m.Mint()
		if id.Compare(prev) <= 0 {
			t.Fatalf("id %d %s compares %d to the one before %s, want greater", i+1, id, id.Compare(prev), prev)
		}
		if id.String() <= prev.String() {
			t.Fatalf("id %d text %s sorts at or before the one before %s", i+1, id, prev)
		}
		prev = id
	}

	now = start.Add(-time.Hour)
	back := m.Mint()
	if back.Compare(prev) <= 0 {
		t.Errorf("id minted after the clock stepped back %s compares %d to the last one %s, want greater", back, back.Compare(prev), prev)
	}
	if back.String() <= prev.String() {
		t.Errorf("id minted after the clock stepped back %s sorts at or before the last one %s", back, prev)
	}
}

func TestAnIDLeadsWithTheClocksUnixMilliseconds(t *testing.T) {
	t.Parallel()

	now := start
	m := minter(t, func() time.Time {
		at := now
		now = now.Add(time.Millisecond)
		return at
	})

	for i := range 1_000 {
		id := m.Mint()
		var lead [8]byte
		copy(lead[2:], id[:6])
		want := start.UnixMilli() + int64(i)
		if got := int64(binary.BigEndian.Uint64(lead[:])); got != want {
			t.Fatalf("id %d %s leads with %d, want the clock's Unix milliseconds %d", i, id, got, want)
		}
	}
}

func TestIDsMintedConcurrentlyAreDistinct(t *testing.T) {
	t.Parallel()

	const goroutines, each = 8, 1_000
	m := minter(t, stalled)

	minted := make([][]sortid.ID, goroutines)
	var wg sync.WaitGroup
	for g := range goroutines {
		wg.Go(func() {
			ids := make([]sortid.ID, each)
			for i := range ids {
				ids[i] = m.Mint()
			}
			minted[g] = ids
		})
	}
	wg.Wait()

	seen := make(map[sortid.ID]struct{}, goroutines*each)
	for _, ids := range minted {
		for _, id := range ids {
			seen[id] = struct{}{}
		}
	}
	if len(seen) != goroutines*each {
		t.Errorf("%d distinct ids out of %d minted", len(seen), goroutines*each)
	}
}

func TestAMintedIDsTextIsTheStdlibsAndParsesBack(t *testing.T) {
	t.Parallel()

	m := minter(t, stalled)
	for range 1_000 {
		id := m.Mint()
		text := id.String()
		if !v7Text.MatchString(text) {
			t.Fatalf("text %q is not a lowercase 8-4-4-4-12 version 7, RFC 9562 variant id", text)
		}
		if std := uuid.UUID(id).String(); text != std {
			t.Fatalf("text %q, want the stdlib's text of the same bytes %q", text, std)
		}
		back, err := sortid.Parse(text)
		if err != nil {
			t.Fatalf("Parse(%q) = %v, want the id it was written from", text, err)
		}
		if back != id {
			t.Fatalf("Parse(%q) = %s, want %s", text, back, id)
		}
	}
}

func TestParseAcceptsTheKnownText(t *testing.T) {
	t.Parallel()

	id, err := sortid.Parse(knownText)
	if err != nil {
		t.Fatalf("Parse(%q) = %v, want an id", knownText, err)
	}
	if got := id.String(); got != knownText {
		t.Errorf("String = %q, want %q", got, knownText)
	}
}

func TestParseRefusesEveryOtherTextUnderOneFixedError(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		"upper-cased":            "0192258C-4A7E-7B3C-9D1E-2F3A4B5C6D7E",
		"mixed case":             "0192258c-4A7e-7b3c-9d1E-2f3a4b5c6d7e",
		"braced":                 "{0192258c-4a7e-7b3c-9d1e-2f3a4b5c6d7e}",
		"urn prefixed":           "urn:uuid:0192258c-4a7e-7b3c-9d1e-2f3a4b5c6d7e",
		"undashed":               "0192258c4a7e7b3c9d1e2f3a4b5c6d7e",
		"leading space":          " 0192258c-4a7e-7b3c-9d1e-2f3a4b5c6d7e",
		"trailing space":         "0192258c-4a7e-7b3c-9d1e-2f3a4b5c6d7e ",
		"one character short":    "0192258c-4a7e-7b3c-9d1e-2f3a4b5c6d7",
		"one character long":     "0192258c-4a7e-7b3c-9d1e-2f3a4b5c6d7e0",
		"one non-hex character":  "0192258c-4a7e-7b3c-9d1e-2f3a4b5c6d7g",
		"a dash out of place":    "0192258c4-a7e-7b3c-9d1e-2f3a4b5c6d7e",
		"version 4":              "0192258c-4a7e-4b3c-9d1e-2f3a4b5c6d7e",
		"version 7, NCS variant": "0192258c-4a7e-7b3c-5d1e-2f3a4b5c6d7e",
		"version 7, Microsoft":   "0192258c-4a7e-7b3c-cd1e-2f3a4b5c6d7e",
		"version 7, future":      "0192258c-4a7e-7b3c-ed1e-2f3a4b5c6d7e",
		"the nil UUID":           "00000000-0000-0000-0000-000000000000",
		"the max UUID":           "ffffffff-ffff-ffff-ffff-ffffffffffff",
		"the empty string":       "",
		"the zero ID's own text": sortid.ID{}.String(),
	}
	for name, text := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			id, err := sortid.Parse(text)
			if !errors.Is(err, sortid.ErrMalformed) {
				t.Fatalf("Parse(%q) = %s, %v, want ErrMalformed", text, id, err)
			}
			if err.Error() != malformed {
				t.Errorf("Parse(%q) refuses with %q, want the fixed %q", text, err.Error(), malformed)
			}
			if id != (sortid.ID{}) {
				t.Errorf("Parse(%q) refused but returned %s, want the zero ID", text, id)
			}
		})
	}
}

func TestAMinterWithoutAClockIsRefusedAtWiring(t *testing.T) {
	t.Parallel()

	m, err := sortid.NewMinter(nil)
	if err == nil {
		t.Fatal("NewMinter(nil) = nil error, want a refusal")
	}
	if m != nil {
		t.Errorf("NewMinter(nil) = %v, want no minter to mint with", m)
	}
}
