// Package password holds a plaintext password in a carrier nothing reads it
// out of.
//
// A login body decodes into the carrier directly, so the secret is inside it
// from the moment it enters the process, instead of passing through a plain
// string field in a struct someone prints:
//
//	type login struct {
//	    Email    string            `json:"email"`
//	    Password password.Password `json:"password"`
//	}
//
// Containment is a property of the type rather than a convention anyone has to
// remember. The plaintext sits behind one indirection, so a struct holding a
// carrier in an unexported field cannot have it reflected out by a formatter
// either, and every surface a value usually leaks through is closed by the
// hook Go offers for it: String for the verbs fmt routes through it, GoString
// for %#v, MarshalJSON and MarshalText for an encoder, LogValue for an slog
// handler. Each answers the same fixed redaction.
//
// Marshalling redacts instead of failing: a service that puts a password in an
// outgoing payload has a bug, and a toolkit answering it with an error decides
// an outage on that service's behalf. What it costs is that the bug ships a
// redacted member rather than stopping at the boundary.
//
// The package writes no log line, reads no environment and contacts nobody. It
// does not promise the plaintext is erased from memory: a Go string is
// immutable and copied by the collector, which is the bound a service holding
// one for longer than a request should know.
package password

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"log/slog"
	"strconv"
)

// ErrEmpty is returned when a carrier is built or decoded from no plaintext at
// all. Refusing it at the entry is what keeps the zero value meaningless:
// nothing derived from a carrier holding nothing can ever verify.
var ErrEmpty = errors.New("password: empty")

// errNotAString answers a JSON value that is not a string. It is a constant
// because the bytes being decoded are the secret itself: nothing read from
// them travels back to the caller, not even the shape the decoder saw.
var errNotAString = errors.New("password: not a JSON string")

// redacted is what every formatting, encoding and logging surface of a carrier
// answers, whatever it holds and whether it holds anything.
const redacted = "[REDACTED]"

// Password carries a plaintext password. Its zero value holds none and matches
// nothing.
//
// The plaintext is behind a pointer on purpose. A formatter reaching a carrier
// through an unexported field cannot call its methods, and reads the fields
// instead; a pointer prints as an address, where a string field would print
// the secret.
type Password struct {
	secret *string
}

// New returns a carrier holding plaintext, and ErrEmpty when there is none.
func New(plaintext string) (Password, error) {
	var p Password
	if err := p.set(plaintext); err != nil {
		return Password{}, err
	}
	return p, nil
}

func (p *Password) set(plaintext string) error {
	if plaintext == "" {
		return ErrEmpty
	}
	p.secret = &plaintext
	return nil
}

// Equal reports whether two carriers hold the same plaintext, comparing in
// constant time so a mismatch says nothing about how far it got. It is how a
// caller compares a password with its confirmation, the plaintext leaving
// neither carrier.
//
// A carrier holding nothing matches nothing, another empty one included.
func (p Password) Equal(other Password) bool {
	if p.secret == nil || other.secret == nil {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(*p.secret), []byte(*other.secret)) == 1
}

// String answers the fixed redaction, which is what %v, %s and %q of a carrier
// print, here and inside any struct holding one in an exported field.
func (p Password) String() string { return redacted }

// GoString answers the fixed redaction under %#v, the one verb fmt does not
// route through String.
func (p Password) GoString() string { return redacted }

// MarshalJSON answers the redaction as a JSON string. A carrier in an outgoing
// payload is a bug in the service, and this ships a redacted member rather
// than failing the response it is a part of.
func (p Password) MarshalJSON() ([]byte, error) {
	return []byte(strconv.Quote(redacted)), nil
}

// MarshalText answers the redaction. The round trip through text is lossy on
// purpose: what MarshalText writes is not a plaintext UnmarshalText accepts
// back.
func (p Password) MarshalText() ([]byte, error) {
	return []byte(redacted), nil
}

// LogValue answers the redaction, so a carrier logged as an attribute reaches
// every slog handler already redacted, the JSON and the text one alike.
func (p Password) LogValue() slog.Value { return slog.StringValue(redacted) }

// UnmarshalJSON reads the plaintext from a JSON string. Anything else, a null
// and an empty string included, is refused rather than decoded into a carrier
// holding nothing.
func (p *Password) UnmarshalJSON(b []byte) error {
	var plaintext string
	if err := json.Unmarshal(b, &plaintext); err != nil {
		return errNotAString
	}
	return p.set(plaintext)
}

// UnmarshalText reads the plaintext from b, and refuses an empty one.
func (p *Password) UnmarshalText(b []byte) error {
	return p.set(string(b))
}
