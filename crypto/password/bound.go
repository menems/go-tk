package password

import (
	"errors"
	"fmt"
)

// ErrBusy answers a hash or a verification that a hasher built by Bound
// refused at once, every one of its places being held. It is an answer about
// the process, not about the password: nothing was compared, and the same
// call made once a place frees may well go through.
//
// It is distinct from ErrMismatch because a caller folding the two shows an
// honest user a wrong password under load, and feeds any lockout it counts
// with attempts that were never checked. It denies like the other two, and is
// never nil. It is one fixed error, the same whatever the password and the
// stored value, and carries no part of either.
var ErrBusy = errors.New("password: every place is held")

// Bound wraps h so that at most places of its calls run at once, and a call
// arriving while every place is held is refused at once under ErrBusy rather
// than queued, h never seeing it.
//
//	hasher, err := password.NewPBKDF2()      // or a Hasher of yours
//	...
//	hasher, err = password.Bound(hasher, 8)  // the service names the number
//
// Hash and Verify draw on the one budget, because both spend the same CPU: a
// flood of sign-ups is a flood of logins as far as the processor goes. A place
// is given back when the call returns, however it returns, a panic included.
//
// What it trades: a burst past the bound is refused, not slowed, so a flood of
// logins, or a few stored values carrying an attacker-written cost, can hold
// every place and have every honest login refused until one frees. Metering
// callers in front of the login (httpd.LimitRate) and bounding the rows a
// service stores are what limit that; this bounds what one process spends, and
// a service on three replicas has three budgets.
//
// A number of places below one is refused here rather than on the first login,
// under an error naming the value: it refuses every call, which is a closed
// login and not a bound. A nil h is refused too, rather than answering the
// first call with a nil dereference.
func Bound(h Hasher, places int) (Hasher, error) {
	if h == nil {
		return nil, errors.New("password: bound over a nil hasher: there is nothing to bound")
	}
	if places < 1 {
		return nil, fmt.Errorf("password: places %d: a bound holding no place refuses every call, which is a closed login and not a bound", places)
	}
	return bounded{h: h, places: make(chan struct{}, places)}, nil
}

// bounded is unexported for the reason pbkdf2Hasher is: its zero value holds a
// nil channel, on which every call would block forever.
//
// A place is a slot of the buffered channel, taken by a send that does not
// wait and given back by a receive. The channel is the one owner of how many
// are held, so no counter and no lock sit beside it.
type bounded struct {
	h      Hasher
	places chan struct{}
}

func (b bounded) take() bool {
	select {
	case b.places <- struct{}{}:
		return true
	default:
		return false
	}
}

func (b bounded) giveBack() { <-b.places }

// Hash hands plaintext to the wrapped hasher when a place is free, and answers
// ErrBusy without reading it when none is.
func (b bounded) Hash(plaintext []byte) (string, error) {
	if !b.take() {
		return "", ErrBusy
	}
	defer b.giveBack()
	return b.h.Hash(plaintext)
}

// Verify hands stored and plaintext to the wrapped hasher when a place is
// free, and answers ErrBusy without reading either when none is.
func (b bounded) Verify(stored string, plaintext []byte) error {
	if !b.take() {
		return ErrBusy
	}
	defer b.giveBack()
	return b.h.Verify(stored, plaintext)
}
