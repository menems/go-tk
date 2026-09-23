package password

import (
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"
)

// DefaultCost is the number of iterations a hasher runs when wiring names
// none: the figure OWASP publishes for PBKDF2-HMAC-SHA256.
//
// A service raising it does not have to rewrite what it stored, the cost of a
// stored value being read back from that value.
const DefaultCost = 600_000

// The shape of a stored value, in the PHC form other password hashes are
// written in: the algorithm that made it, the parameters it was made under,
// then the salt and the derived key, base64 without padding.
//
//	$pbkdf2-sha256$i=600000$<salt>$<key>
//
// It is documented because a service stores it and migrates it. It is not a
// format this package reads from anywhere else: another Hasher writes its own
// shape, and a value carrying another algorithm is one this one refuses.
const (
	algorithm = "pbkdf2-sha256"
	costField = "i="

	// 128 bits of salt, and the 256 the hash named in the algorithm field
	// outputs. Both are fixed, so a value carrying another length is one this
	// package did not write.
	saltLen = 16
	keyLen  = sha256.Size
)

var encoding = base64.RawStdEncoding

// Option is what NewPBKDF2 takes at wiring. WithCost is the only one.
type Option func(*pbkdf2Hasher)

// WithCost sets the number of iterations the hasher runs, which is what a
// service raises as hardware gets faster. Below one it is refused by
// NewPBKDF2.
func WithCost(cost int) Option {
	return func(h *pbkdf2Hasher) { h.cost = cost }
}

// NewPBKDF2 builds the Hasher this package ships, PBKDF2-HMAC-SHA256 at
// DefaultCost unless WithCost names another.
//
//	hasher, err := password.NewPBKDF2()        // or WithCost(n)
//	if err != nil {
//	    return fmt.Errorf("password hasher: %w", err)
//	}
//	svc := users.New(store, hasher)            // the service wires it, once
//
// It derives its key by iteration alone, which is the memory-cheap one of the
// three schemes anyone recommends: a stolen table is worth more to an attacker
// with GPUs here than under bcrypt or argon2id. That is the price of this
// toolkit's root module holding nothing outside the standard library. A
// service whose table would be worth cracking writes a bcrypt or an argon2id
// Hasher at its own wiring, in its own module, where that dependency belongs,
// and nothing else about the service changes.
//
// A cost below one is refused here rather than on the first login, under an
// error naming the value: a hasher iterating fewer than once stores a key an
// attacker reads back as fast as it can hash, which is no derivation at all.
// There is no ceiling: the cost a service is allowed to raise to is the
// service's own question, and the same number has to be readable back out of
// everything it already stored.
func NewPBKDF2(opts ...Option) (Hasher, error) {
	h := pbkdf2Hasher{cost: DefaultCost}
	for _, opt := range opts {
		opt(&h)
	}
	if h.cost < 1 {
		return nil, fmt.Errorf("password: cost %d: a hasher iterating fewer than once stores a key an attacker reads back as fast as it can hash", h.cost)
	}
	return h, nil
}

// pbkdf2Hasher is unexported so that the only hasher a service can hold is one
// NewPBKDF2 handed it: an exported struct would have a zero value, and the
// zero value of this one hashes at no iterations at all. Same shape as
// httpd.CountWithin, which hands back its seam type over an unexported
// implementation.
type pbkdf2Hasher struct {
	cost int
}

// Hash derives a key under a salt of its own, and writes both into the stored
// value along with the cost they were made under.
func (h pbkdf2Hasher) Hash(plaintext []byte) (string, error) {
	salt := make([]byte, saltLen)
	// Never returns an error: crypto/rand fills the slice entirely or takes
	// the process down.
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("password: read salt: %w", err)
	}

	key, err := pbkdf2.Key(sha256.New, string(plaintext), salt, h.cost, keyLen)
	if err != nil {
		// The text is the derivation's own and describes the parameters this
		// package chose, never the password it was handed.
		return "", fmt.Errorf("password: derive: %w", err)
	}

	return strings.Join([]string{
		"",
		algorithm,
		costField + strconv.Itoa(h.cost),
		encoding.EncodeToString(salt),
		encoding.EncodeToString(key),
	}, "$"), nil
}

// Verify derives a key from plaintext under the salt and the cost stored
// carries, and compares it with the key stored carries.
//
// The cost is the stored value's own and not this hasher's, which is what lets
// a service raise its cost and still verify everything it stored before.
func (h pbkdf2Hasher) Verify(stored string, plaintext []byte) error {
	cost, salt, key, err := parse(stored)
	if err != nil {
		return err
	}

	got, err := pbkdf2.Key(sha256.New, string(plaintext), salt, cost, keyLen)
	if err != nil {
		// A stored value that parsed but that the derivation refuses is one
		// this verification could not run on, not a password that was wrong.
		return ErrUnreadable
	}
	if subtle.ConstantTimeCompare(got, key) != 1 {
		return ErrMismatch
	}
	return nil
}

// parse reads a stored value before anything trusts it, and answers
// ErrUnreadable for every shape this package did not write. The error says
// only that, never which field failed nor what it held: a stored value is a
// secret's neighbour, and an operator's question is answered by the row, not
// by the refusal.
//
// The lengths are checked and not merely decoded. The algorithm field names
// the hash and the salt width, both fixed, so a value carrying another length
// is one this package did not write and saying so is the truthful answer: a
// row truncated in the store would otherwise reach the comparison and come
// back as a password that was wrong, which sends an operator looking at the
// user instead of at the row. What keeps that row from matching anything is
// separate and holds either way, the derivation length being this package's
// own constant and never a number read out of the value.
func parse(stored string) (int, []byte, []byte, error) {
	f := strings.Split(stored, "$")
	if len(f) != 5 || f[0] != "" || f[1] != algorithm {
		return 0, nil, nil, ErrUnreadable
	}

	digits, ok := strings.CutPrefix(f[2], costField)
	if !ok {
		return 0, nil, nil, ErrUnreadable
	}
	cost, err := strconv.Atoi(digits)
	if err != nil || cost < 1 {
		return 0, nil, nil, ErrUnreadable
	}

	salt, err := encoding.DecodeString(f[3])
	if err != nil || len(salt) != saltLen {
		return 0, nil, nil, ErrUnreadable
	}

	key, err := encoding.DecodeString(f[4])
	if err != nil || len(key) != keyLen {
		return 0, nil, nil, ErrUnreadable
	}

	return cost, salt, key, nil
}
