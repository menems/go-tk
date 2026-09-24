// Package token issues an opaque bearer token, and parses it back when it
// arrives on a request.
//
// A token is 256 bits drawn from crypto/rand. Its text leaves the carrier
// through Reveal alone, for the response that hands it to the client once.
// What a service writes to disk is the stored value: the SHA-256 of the
// secret, deterministic so the service finds its row by it through a unique
// index, and replaying nothing, since the secret's entropy leaves no preimage
// to search. A leaked table or a logged row opens no session.
//
// The stored value is not a password.Hasher's: a salted, slow derivation
// cannot be looked up by value, so it would force a selector into the token
// and spend on every request a hash's CPU, to protect a secret that already
// carries 256 bits.
//
// Every other surface a value leaks through answers one fixed redaction, by
// the hook Go offers for it, as a password.Password does: String for the verbs
// fmt routes through it, GoString for %#v, MarshalJSON and MarshalText for an
// encoder, LogValue for an slog handler. A request body decodes into the
// carrier directly, by UnmarshalJSON or UnmarshalText, each parsing like Parse
// and refusing under ErrMalformed.
//
// Parsing identifies nobody and grants nothing. A token that parses is well
// formed, not valid: the verdict is the service's row, found by the stored
// value and carrying its own expiry and revocation. A resolver treating a
// parse success as authentication fails open.
//
// The package is named token and shadows go/token, which a service has no use
// for. It writes no log line and does not erase the text from memory.
package token

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"strconv"
)

// ErrMalformed answers a text this package did not issue. It is one fixed
// error whatever arrived, and carries no part of it: the text is a secret, and
// a refusal telling one wrong text from another would say how close a guess
// came.
var ErrMalformed = errors.New("token: not a token this package issues")

// ErrEmpty answers a stored value asked of a carrier holding no token, its
// zero value. Answering one would give a service a row nothing ever issued.
var ErrEmpty = errors.New("token: the carrier holds no token")

// redacted is what every formatting, encoding and logging surface of a carrier
// answers, whatever it holds and whether it holds anything.
const redacted = "[REDACTED]"

// size is the secret's length in bytes: 256 bits.
const size = 32

// encoding is URL-safe so the text travels in a header, a query or a cookie
// as it is, and unpadded because the length is fixed and says it already.
var encoding = base64.RawURLEncoding

// textLen is the length of every text Issue hands out, 43.
var textLen = encoding.EncodedLen(size)

// Token carries a secret this package issued or parsed. Its zero value holds
// none, reveals no text and yields no stored value.
//
// The secret is behind a pointer, as a password.Password's is: a formatter
// reaching a carrier through an unexported field reads its fields, and a
// pointer prints as an address where an array would print the secret.
type Token struct {
	secret *[size]byte
}

// Issue draws a fresh token.
func Issue() Token {
	var secret [size]byte
	// crypto/rand.Read never returns an error: a platform that cannot give
	// randomness stops the process instead of handing out a guessable token.
	_, _ = rand.Read(secret[:])
	return Token{secret: &secret}
}

// Parse reads a token off the text a request carried, and refuses under
// ErrMalformed anything that is not a text Issue could have handed out.
//
// The length is checked first and is fixed by the format, so what a parse
// costs does not grow with what arrives.
func Parse(text string) (Token, error) {
	if len(text) != textLen {
		return Token{}, ErrMalformed
	}
	var secret [size]byte
	n, err := encoding.Decode(secret[:], []byte(text))
	// The decoder skips line breaks and ignores the unused low bits of the
	// last character, so several texts decode to one secret. Only the one
	// Issue would have written is accepted: a service keying anything on the
	// text sees one text per token.
	if err != nil || n != size || encoding.EncodeToString(secret[:]) != text {
		return Token{}, ErrMalformed
	}
	return Token{secret: &secret}, nil
}

// Reveal returns the token's text, the one way it leaves the carrier: for the
// response that hands it to the client, once. The zero value reveals "".
func (t Token) Reveal() string {
	if t.secret == nil {
		return ""
	}
	return encoding.EncodeToString(t.secret[:])
}

// Stored returns the value a service writes to disk and looks the token up
// by: the hex SHA-256 of the secret, 64 characters, which Parse refuses.
func (t Token) Stored() (string, error) {
	if t.secret == nil {
		return "", ErrEmpty
	}
	sum := sha256.Sum256(t.secret[:])
	return hex.EncodeToString(sum[:]), nil
}

// String answers the fixed redaction, which is what %v, %s and %q of a carrier
// print, here and inside any struct holding one in an exported field.
func (t Token) String() string { return redacted }

// GoString answers the fixed redaction under %#v, the one verb fmt does not
// route through String.
func (t Token) GoString() string { return redacted }

// MarshalJSON answers the redaction as a JSON string, not the text: the
// response handing a token out once writes Reveal into its own field. A
// carrier in an outgoing payload is a bug in the service, and this ships a
// redacted member rather than failing the response it is a part of.
func (t Token) MarshalJSON() ([]byte, error) {
	return []byte(strconv.Quote(redacted)), nil
}

// MarshalText answers the redaction. The round trip through text is lossy on
// purpose: what MarshalText writes is not a text UnmarshalText accepts back.
func (t Token) MarshalText() ([]byte, error) {
	return []byte(redacted), nil
}

// LogValue answers the redaction, so a carrier logged as an attribute reaches
// every slog handler already redacted, the JSON and the text one alike.
func (t Token) LogValue() slog.Value { return slog.StringValue(redacted) }

// UnmarshalJSON reads a token off a JSON string, parsed like Parse. A null, a
// value that is not a string and a string Parse refuses are each refused under
// ErrMalformed, and leave the carrier as it was.
func (t *Token) UnmarshalJSON(b []byte) error {
	var text string
	if err := json.Unmarshal(b, &text); err != nil {
		return ErrMalformed
	}
	return t.UnmarshalText([]byte(text))
}

// UnmarshalText reads a token off b, parsed like Parse, and leaves the carrier
// as it was when Parse refuses it.
func (t *Token) UnmarshalText(b []byte) error {
	parsed, err := Parse(string(b))
	if err != nil {
		return err
	}
	*t = parsed
	return nil
}
