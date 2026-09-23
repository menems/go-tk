package password

import "errors"

// The two ways a verification refuses, and the whole of the seam's error
// contract. They are distinct because a caller does different things with
// them: ErrMismatch is an answer about a password and denies the login,
// ErrUnreadable is the verification never having run and denies it too, while
// naming a row nobody can authenticate against until it is rewritten. A caller
// treating the second as the first watches an unreadable store look like a
// user who keeps mistyping; one treating either as success fails open, which
// is why neither is ever nil.
//
// ErrMismatch is the same error whatever the password was, and neither carries
// any part of the password or of the stored value: what a caller learns is
// that the two do not go together, and nothing about how far the comparison
// got.
var (
	ErrMismatch   = errors.New("password: mismatch")
	ErrUnreadable = errors.New("password: unreadable stored value")
)

// Hasher derives the value a service stores from a plaintext password, and
// says whether a stored value and a plaintext go together. One implementation
// ships here, NewPBKDF2; a service names at wiring which one it runs on, and
// this package picks none on its own.
//
// Verify answers nil when they go together, ErrMismatch when they do not, and
// ErrUnreadable for a stored value it cannot read: one another implementation
// wrote, one truncated, one carrying parameters it does not understand, an
// empty one. Hash and Verify are one interface rather than two because a
// stored value only means anything to the implementation that wrote it.
//
// The stored value is an opaque string on purpose. Its shape belongs to the
// implementation, not to this package: an argon2id hasher writes its own, and
// nothing here parses a value it did not write. A service stores it as it
// received it and hands it back unchanged.
//
// It takes the plaintext as bytes and not as a Password because a Password
// reads out to nobody, this package included from the outside: an
// implementation living in a service's own module could not open one. This is
// the one place the plaintext leaves the carrier, and it leaves it into the
// hasher the service chose and nowhere else. The bytes are a copy the
// implementation may overwrite; it must not retain them.
//
// An implementation is called from every request that logs a user in, so it
// must be safe for concurrent use.
type Hasher interface {
	Hash(plaintext []byte) (stored string, err error)
	Verify(stored string, plaintext []byte) error
}

// Hash derives through h the value a service stores for this password.
//
// A carrier holding nothing, the zero value among them, is refused under
// ErrEmpty rather than hashed: h is never reached, so no stored value can be
// derived from a carrier nobody filled.
func (p Password) Hash(h Hasher) (string, error) {
	if p.secret == nil {
		return "", ErrEmpty
	}
	return h.Hash([]byte(*p.secret))
}

// Verify asks h whether stored and this password go together, answering nil
// when they do and h's own refusal when they do not.
//
// A carrier holding nothing is refused under ErrEmpty rather than compared, so
// a caller that forgot to fill one is told that, not that the password was
// wrong.
func (p Password) Verify(h Hasher, stored string) error {
	if p.secret == nil {
		return ErrEmpty
	}
	return h.Verify(stored, []byte(*p.secret))
}
