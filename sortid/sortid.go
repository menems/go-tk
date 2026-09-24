// Package sortid mints identifiers that sort in the order they were minted,
// and parses back only the text they are written in.
//
// An ID is a version 7 UUID of RFC 9562: 48 bits of Unix milliseconds, the
// version, 12 bits of the millisecond's fraction, the variant and 62 bits from
// crypto/rand. Its bytes and its text compare in the same order, so a row keyed
// on it lands at the end of its index.
//
// A Minter is built at wiring with the service's clock, time.Now in main, and
// is not the stdlib's uuid.NewV7 behind a function: that reads its own clock,
// so no test can pin the order a stalled or backward clock gives, and on a
// backward step it starts over and the order breaks. Nor is there a
// package-level default minter, which would be global state with its clock set
// before first use by convention alone.
//
// An ID identifies a row and grants nothing: its random bits make it hard to
// guess, but it is no capability, and a service reading one off a request
// authorizes access to that row itself. It publishes its creation time to the
// millisecond to anyone who reads it.
package sortid

import (
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"errors"
	"sync"
	"time"
	"uuid"
)

// ErrMalformed answers a text that is not one String writes for a version 7,
// RFC 9562 variant id. It is one fixed error whatever arrived, and carries no
// part of it, so a refusal never echoes a request back into a log or a body.
var ErrMalformed = errors.New("sortid: not an id this package mints")

// textLen is the length of every text String writes, 8-4-4-4-12 hex digits.
const textLen = 36

// ID is a version 7 UUID, over the 16 bytes of the stdlib's uuid.UUID. A
// mapper converts a row's bytes with ID(u) and back with uuid.UUID(id), without
// a call: that conversion trusts the service's own row, and Parse is the one
// checked entry from outside. Its zero value is the nil UUID, which Parse
// refuses.
type ID uuid.UUID

// String writes the stdlib's lowercase 8-4-4-4-12 text of the id.
func (id ID) String() string { return uuid.UUID(id).String() }

// Compare returns -1, 0 or +1 as id sorts before, with or after other, in the
// order the ids were minted.
func (id ID) Compare(other ID) int { return uuid.UUID(id).Compare(uuid.UUID(other)) }

// Parse reads an id off the text a request carried, and refuses under
// ErrMalformed anything but the text String writes for a version 7, RFC 9562
// variant id.
//
// The length is checked first and is fixed by the format, so what a parse
// costs does not grow with what arrives.
func Parse(text string) (ID, error) {
	if len(text) != textLen {
		return ID{}, ErrMalformed
	}
	u, err := uuid.Parse(text)
	// The stdlib reads any case, so an upper-cased text parses to the same
	// bytes. Only the text String writes is accepted: a service keying
	// anything on the text sees one text per id.
	if err != nil || u.String() != text {
		return ID{}, ErrMalformed
	}
	if !ID(u).minted() {
		return ID{}, ErrMalformed
	}
	return ID(u), nil
}

// minted reports whether id is of version 7 and RFC 9562 variant, the ids a
// Minter mints and the only ones Parse accepts.
func (id ID) minted() bool { return id[6]>>4 == 7 && id[8]>>6 == 0b10 }

// IsZero reports whether id is the zero ID, so a field tagged omitzero drops
// it rather than failing the whole payload on MarshalText.
func (id ID) IsZero() bool { return id == ID{} }

// MarshalText writes the text String writes, and only a text Parse accepts
// back: the zero ID, or bytes of another version converted from a row, fail
// under ErrMalformed rather than write an id a reader would refuse. A map
// keyed by ID marshals its keys through it, and a struct field as a JSON
// string.
func (id ID) MarshalText() ([]byte, error) {
	if !id.minted() {
		return nil, ErrMalformed
	}
	return []byte(id.String()), nil
}

// UnmarshalJSON reads an id off a JSON string, parsed like Parse. A null, a
// value that is not a string and a string Parse refuses are each refused under
// ErrMalformed, and leave the id as it was. Without it, encoding/json would
// skip a null and decode an array of 16 numbers into the bytes unchecked.
func (id *ID) UnmarshalJSON(b []byte) error {
	var text string
	if err := json.Unmarshal(b, &text); err != nil {
		return ErrMalformed
	}
	return id.UnmarshalText([]byte(text))
}

// UnmarshalText reads an id off b, parsed like Parse rather than like the
// stdlib's UUID.UnmarshalText, which reads four forms in any case. It leaves
// the id as it was when Parse refuses it.
func (id *ID) UnmarshalText(b []byte) error {
	parsed, err := Parse(string(b))
	if err != nil {
		return err
	}
	*id = parsed
	return nil
}

// Minter mints ids from one clock. It is safe for concurrent use.
type Minter struct {
	now func() time.Time

	mu sync.Mutex
	// last is the 60-bit timestamp of the id minted last: Unix milliseconds
	// shifted left 12, plus the fraction of the millisecond in 4,096ths.
	last uint64
}

// NewMinter builds a Minter over now, the service's clock: time.Now everywhere
// but a test. A nil clock is refused here rather than on the first mint.
func NewMinter(now func() time.Time) (*Minter, error) {
	if now == nil {
		return nil, errors.New("sortid: a minter needs a clock, time.Now in main")
	}
	return &Minter{now: now}, nil
}

// Mint returns an id greater than every one this Minter minted before.
//
// When the clock stands still or steps back, the timestamp moves one 4,096th
// of a millisecond past the last one instead, so the order holds and the ids
// run ahead of the clock by that much per id until it catches up.
func (m *Minter) Mint() ID {
	t := m.now()
	ts := uint64(t.UnixMilli())<<12 | uint64(t.Nanosecond()%1_000_000)*4096/1_000_000

	m.mu.Lock()
	if ts <= m.last {
		ts = m.last + 1
	}
	m.last = ts
	m.mu.Unlock()

	var u uuid.UUID
	// The 48 bits of milliseconds, a gap for the version, then the 12 of the
	// fraction.
	binary.BigEndian.PutUint64(u[0:8], ts<<4&^0xffff|ts&0x0fff)
	// crypto/rand.Read never returns an error: a platform that cannot give
	// randomness stops the process instead.
	_, _ = rand.Read(u[8:])
	u[6] = u[6]&0x0f | 0x70
	u[8] = u[8]&0x3f | 0x80
	return ID(u)
}
