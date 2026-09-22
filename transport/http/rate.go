package httpd

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// The codes a metered route refuses with: CodeTooManyRequests when the caller
// of this request has no call left, CodeRateUnavailable when its counter could
// not answer whether it had one. A code of its own rather than
// CodeAuthUnavailable: what failed is a budget and not a credential, and a
// client retrying one is not retrying the other.
const (
	CodeTooManyRequests ErrorCode = "too_many_requests"
	CodeRateUnavailable ErrorCode = "rate_unavailable"
)

// Fixed, like the rest of the envelope's messages, and they name neither the
// caller that was metered, nor the ceiling it met, nor what it has left: the
// first tells a caller nothing it does not already know, the second and third
// answer a question it never asked and describe another caller's budget the
// day two share a key.
const (
	messageTooManyRequests = "this caller has made too many requests"
	messageRateUnavailable = "this service could not answer whether this request is within its limits"
)

// The header a refusal names the moment to come back in, as the delay in
// seconds that RFC 9110 spells.
const headerRetryAfter = "Retry-After"

// CallerKey names the caller a request belongs to, and answers false for a
// request belonging to none.
//
// It is the service's own, because every honest key is a fact this package
// holds nothing of: an address means deciding which forwarded header to trust,
// and a principal means a key only the service names. Nothing of the request
// is read here, so a key a caller can forge is a key with which it spends
// another caller's budget, and a service keying on a forwarded address trusts
// exactly its own proxy or meters that proxy as one caller.
//
// A request it names no caller for is served with no call taken, which is the
// price of the key being the service's: a route reachable with no credential
// and keyed on its principal is metered for nobody.
type CallerKey[K any] func(r *http.Request) (K, bool)

// Counter takes one call from the budget of key, and answers whether there was
// one to take, with the delay before the next one.
//
// The error says which of two things happened. A nil error is a verdict: the
// bool carries it, true serves the request, false refuses it 429, and the
// delay is read on that refusal alone. Any other error is the counter failing
// rather than answering, the bool is not read, and the request is answered
// 500, that being the same trade Resolver and Holds make and for the same
// reason: a budget store that is down reported as a caller that was too eager
// hides the outage behind a status that reads as the caller's own fault, and
// tells it to come back to a service that has not counted it.
//
// It is the service's own: a ceiling, a window and where a budget lives are
// exactly where services differ, and a counter of a single process gives each
// replica a budget of its own, which is why nothing here holds one. A shared
// store, its module and its dependency are the service's decision, taken
// behind this seam and not here.
//
// ctx is the request's, so a lookup it makes is cancelled with the request
// that asked for it.
type Counter[K any] func(ctx context.Context, key K) (bool, time.Duration, error)

// LimitRate covers the handler of a route a service meters: the caller key
// names who the request belongs to, count takes one call from that caller's
// budget, and the handler runs only when count answers that there was one.
//
//	meter := httpd.LimitRate(callerOf, count) // both are yours
//	h, err := httpd.NewRouter(
//	    httpd.Route{Method: http.MethodGet, Pattern: "/things", Handler: auth(meter(list))},
//	    httpd.Route{Method: http.MethodGet, Pattern: "/status", Handler: auth(status)},
//	)
//
// A request whose caller has no call left is answered 429 under
// CodeTooManyRequests, with a Retry-After header naming a whole number of
// seconds, rounded up from the delay count gave and never below one: a caller
// coming back on the second it was told finds the call it was promised, and a
// zero would invite it back at once. One whose count failed instead of
// answering is answered 500 under CodeRateUnavailable, with no such header,
// nothing having been counted and no moment existing to send it back to. A
// request caller names no caller for is served, count being asked nothing. The
// wrapped handler runs in neither refusal.
//
// Both refusals are one fixed body under one code, the same whatever caller
// was metered, whatever its ceiling and whatever its delay; neither carries
// the key, and the one number either carries is that caller's own delay, in a
// header. So a ceiling here is measured by probing and is not a secret, and a
// client tells the refusal it should wait out from the one it should report by
// the status and the code alone.
//
// The wrap sits on the handler, so the order a request meets is the route
// table, then the bearer, then the right, then the rate: no budget is touched
// for a request no route matched, none is spent on the 404s of a path nobody
// named, and the preflight Grant.Router answers is answered before any wrap on
// a handler runs. A service whose key is readable from the request alone can
// place it outside the bearer wrap instead, the one position where a flood of
// unresolvable tokens is metered before the resolver is asked.
func LimitRate[K any](caller CallerKey[K], count Counter[K]) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			key, ok := caller(r)
			if !ok {
				next.ServeHTTP(w, r)
				return
			}

			left, delay, err := count(r.Context(), key)
			if err != nil {
				// The text is the counter's own and may name the caller it was
				// asked about or the store it read, so it reaches neither the
				// answer nor a log here: what an operator has to go on is the
				// status and the code. The verdict beside it is not read: a
				// counter that reached none has none to give.
				WriteError(w, http.StatusInternalServerError, CodeRateUnavailable, messageRateUnavailable)
				return
			}
			if !left {
				w.Header().Set(headerRetryAfter, strconv.FormatInt(retryAfter(delay), 10))
				WriteError(w, http.StatusTooManyRequests, CodeTooManyRequests, messageTooManyRequests)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

// retryAfter renders d as the whole number of seconds a Retry-After header
// carries. It rounds up, so the moment a caller is sent back to is one the
// budget has reached, and it floors at one: a delay of zero, or one a counter
// answered about a window already past, would otherwise be a refusal telling
// the caller to retry immediately.
func retryAfter(d time.Duration) int64 {
	if d <= time.Second {
		return 1
	}
	seconds := int64(d / time.Second)
	if d%time.Second != 0 {
		seconds++
	}
	return seconds
}

// CountWithin builds the Counter of a single process: ceiling calls for each
// caller key inside window, and the window of a key opening on that key's own
// first call rather than on a boundary this package picks.
//
//	count, err := httpd.CountWithin[callerKey](100, time.Minute, time.Now)
//	meter := httpd.LimitRate(callerOf, count) // callerOf is yours
//
// now is the service's clock, time.Now everywhere but a test: nothing here
// reads a clock of its own, so a test meters over a window of minutes and
// passes it rather than waiting one out.
//
// Its budgets live in the memory of the process serving the requests, so a
// service on three replicas meters three cadences of ceiling calls and not
// one. That is the trade of the counter being a seam: a budget shared across
// replicas is a store, its module and its dependency, and which one is the
// service's decision, taken behind Counter and not here. What this one holds
// grows with the distinct keys seen inside two windows at most, an entry being
// forgotten by a sweep that runs at most once per window rather than at the
// instant that key's own window passed, so a caller rotating its key faster
// than the window drives that growth and a service needing a harder bound
// hands in a counter of its own.
//
// A ceiling below one call, and a window of zero or less, are refused here
// rather than on the first request, under an error naming the value: the first
// meters a route into a closed one, and the second names no moment a refused
// caller could come back at.
func CountWithin[K comparable](ceiling int, window time.Duration, now func() time.Time) (Counter[K], error) {
	if ceiling < 1 {
		return nil, fmt.Errorf("httpd: ceiling %d: a cadence granting no call at all refuses every request, which is a closed route and not a rate", ceiling)
	}
	if window <= 0 {
		return nil, fmt.Errorf("httpd: window %s: a cadence over no time at all names no moment a refused caller comes back at", window)
	}

	c := &cadence[K]{ceiling: ceiling, window: window, now: now, seen: make(map[K]spent)}
	return c.count, nil
}

// spent is what one key has taken of the window it opened: the instant that
// window began, and the calls taken since.
type spent struct {
	opened time.Time
	calls  int
}

// cadence is the state CountWithin closes over. Every request the server has
// in flight reaches the same map, so both what it holds and when it was last
// swept are read and written under the one mutex.
type cadence[K comparable] struct {
	ceiling int
	window  time.Duration
	now     func() time.Time

	mu    sync.Mutex
	seen  map[K]spent
	swept time.Time
}

// count takes one call from the window of key, opening a fresh one for a key
// that has none or whose own has passed. ctx is unread: the answer comes out
// of the memory of the process that asked, and there is no lookup to cancel.
func (c *cadence[K]) count(_ context.Context, key K) (bool, time.Duration, error) {
	at := c.now()

	c.mu.Lock()
	defer c.mu.Unlock()

	c.forget(at)

	w, seen := c.seen[key]
	if !seen || !at.Before(w.opened.Add(c.window)) {
		w = spent{opened: at}
	}
	if w.calls >= c.ceiling {
		// What is left of the window this key opened, which is above zero
		// because the window has not passed, and no longer than the window
		// itself because it opened no later than now.
		return false, w.opened.Add(c.window).Sub(at), nil
	}
	w.calls++
	c.seen[key] = w
	return true, 0, nil
}

// forget drops the keys whose window has passed, once per window rather than
// once per request: what the map then holds is the keys seen since the sweep
// before it, and a caller that never comes back is not held for the life of
// the process. A sweep on every request would instead be one pass over every
// key seen, at the rate the callers themselves set.
func (c *cadence[K]) forget(at time.Time) {
	if at.Before(c.swept.Add(c.window)) {
		return
	}
	for key, w := range c.seen {
		if !at.Before(w.opened.Add(c.window)) {
			delete(c.seen, key)
		}
	}
	c.swept = at
}
