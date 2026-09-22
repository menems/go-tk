package httpd_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode"

	"github.com/menems/go-tk/transport/http"
)

// callerKey stands for whatever a service meters by: a principal of its own, a
// tenant, an address it trusts its proxy for. It is the service's type, and
// this package never reads it.
type callerKey string

// The header this stand-in service reads its caller from, and the three
// callers below. All three carry a marker, so an answer repeating any of them
// is caught by the no-echo assertions.
const headerCaller = "X-Marker-Caller"

const (
	oneCaller    callerKey = "marker-caller-one"
	otherCaller  callerKey = "marker-caller-two"
	brokenCaller callerKey = "marker-caller-broken"
)

// The two refusals a metered route gives, written here as the literals a
// client reads.
const (
	messageTooManyRequests = "this caller has made too many requests"
	messageRateUnavailable = "this service could not answer whether this request is within its limits"
)

// The text the counter puts in its own error, and what the no-echo assertions
// look for in the answer that request gets: the counter's own innards.
const counterFailureText = "marker-budget store unreachable"

// callerOf is the service's own keying function: it names the caller a request
// belongs to, and names none for a request carrying no such header. This
// package compares no header and reads no address of its own, so which one is
// trusted is decided here.
func callerOf(r *http.Request) (callerKey, bool) {
	c := r.Header.Get(headerCaller)
	if c == "" {
		return "", false
	}
	return callerKey(c), true
}

// counter stands for the service's own budget: it answers whether the caller
// it is handed has a call left and when it next will, and answers one caller
// with a failure of its own instead of an answer. It records how many times it
// was asked anything, so a request that must reach no counter can be checked
// to have reached none.
//
// left is what each caller may still spend and is taken from under a lock: the
// requests below are served on the server's own goroutines.
type counter struct {
	mu    sync.Mutex
	left  map[callerKey]int
	delay map[callerKey]time.Duration
	calls atomic.Int64
}

// count answers a verdict for every caller but the broken one, which stands
// for the budget store being down: that answer says a call is left beside the
// error, so a wrap reading the verdict of a counter that reached none serves a
// request this one never allowed.
func (c *counter) count(_ context.Context, key callerKey) (bool, time.Duration, error) {
	c.calls.Add(1)
	if key == brokenCaller {
		return true, time.Hour, errors.New(counterFailureText)
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.left[key] <= 0 {
		return false, c.delay[key], nil
	}
	c.left[key]--
	return true, 0, nil
}

// metered is one table of two routes: one the service meters and one it does
// not, each with its own handler, plus the counter the meter was given.
type metered struct {
	srv     *httptest.Server
	counter *counter
	things  *marker
	open    *marker
}

// meter builds that table over c. This is the seam: a consumer meets the wrap
// through the handler NewRouter returns, one served request at a time.
func meter(t *testing.T, c *counter) metered {
	t.Helper()

	things, open := &marker{name: "things"}, &marker{name: "status"}
	srv := gate(t,
		httpd.Route{
			Method:  http.MethodGet,
			Pattern: "/things",
			Handler: httpd.LimitRate(callerOf, c.count)(things),
		},
		httpd.Route{Method: http.MethodGet, Pattern: "/status", Handler: open},
	)

	return metered{srv: srv, counter: c, things: things, open: open}
}

// budget builds a counter granting each named caller one call and telling it
// to come back in delay once that call is spent.
func budget(delay time.Duration, callers ...callerKey) *counter {
	c := &counter{
		left:  make(map[callerKey]int, len(callers)),
		delay: make(map[callerKey]time.Duration, len(callers)),
	}
	for _, k := range callers {
		c.left[k] = 1
		c.delay[k] = delay
	}
	return c
}

// withCaller sets the header this service meters by, and leaves it unset for
// an empty value, which is the request belonging to no caller at all.
func withCaller(req *http.Request, caller callerKey) *http.Request {
	if caller != "" {
		req.Header.Set(headerCaller, string(caller))
	}
	return req
}

// asking builds a request for path on srv, from caller.
func asking(t *testing.T, srv *httptest.Server, path string, caller callerKey) *http.Request {
	t.Helper()

	return withCaller(ask(t, srv, http.MethodGet, path, nil), caller)
}

// assertSaysNothingOfTheBudget checks an answer names none of the callers in
// play, nothing the counter itself said, and no number at all: a ceiling, a
// count left and a delay are each something a caller would otherwise read off
// the body it gets.
func assertSaysNothingOfTheBudget(t *testing.T, answer []byte) {
	t.Helper()

	for _, k := range []callerKey{oneCaller, otherCaller, brokenCaller} {
		assertNoEcho(t, answer, string(k))
	}
	assertNoEcho(t, answer, counterFailureText)
	if strings.ContainsFunc(string(answer), unicode.IsDigit) {
		t.Errorf("answer carries a number: %s", answer)
	}
}

// assertTooManyRequests checks the answer is the wrap's 429 envelope, says
// nothing of the budget, and tells the caller when to come back.
func assertTooManyRequests(t *testing.T, status int, answer []byte, header http.Header, wantRetry string) {
	t.Helper()

	assertRefusal(t, status, answer, http.StatusTooManyRequests, httpd.CodeTooManyRequests, messageTooManyRequests)
	assertSaysNothingOfTheBudget(t, answer)
	assertHeader(t, header, "Retry-After", wantRetry)
}

// assertRateUnavailable checks the answer is the wrap's 500 envelope, under a
// code of its own, and carries no delay: nothing was counted, so there is no
// moment to send the caller back to.
func assertRateUnavailable(t *testing.T, status int, answer []byte, header http.Header) {
	t.Helper()

	assertRefusal(t, status, answer, http.StatusInternalServerError, httpd.CodeRateUnavailable, messageRateUnavailable)
	assertSaysNothingOfTheBudget(t, answer)
	assertHeader(t, header, "Retry-After", "")
}

func TestRateReachesTheHandlerOfACallerWithACallLeft(t *testing.T) {
	t.Parallel()

	m := meter(t, budget(time.Minute, oneCaller))

	status, answer := send(t, m.srv, asking(t, m.srv, "/things", oneCaller))

	assertServed(t, status, answer, "things")
	if got := m.counter.calls.Load(); got != 1 {
		t.Errorf("counter asked %d times, want 1", got)
	}
}

// TestRateRefusesACallerWithNoCallLeft pins the acceptance: the call the
// budget held is spent by the first request, and the second is the one this
// wrap exists to answer.
func TestRateRefusesACallerWithNoCallLeft(t *testing.T) {
	t.Parallel()

	m := meter(t, budget(90*time.Second, oneCaller))

	status, answer := send(t, m.srv, asking(t, m.srv, "/things", oneCaller))
	assertServed(t, status, answer, "things")

	status, answer, header := exchange(t, m.srv, asking(t, m.srv, "/things", oneCaller))

	assertTooManyRequests(t, status, answer, header, "90")
	if got := m.counter.calls.Load(); got != 2 {
		t.Errorf("counter asked %d times, want 2", got)
	}
}

// TestRateTellsACallerPastItsCadenceFromACounterAnsweringNothing puts the two
// refusals side by side: a client branches on the status and the code, and on
// nothing else, because nothing of the caller, of its budget, or of what the
// counter said is in either answer. The handler runs in neither.
func TestRateTellsACallerPastItsCadenceFromACounterAnsweringNothing(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		caller callerKey
		assert func(*testing.T, int, []byte, http.Header)
	}{
		{
			name:   "a caller past its cadence",
			caller: oneCaller,
			assert: func(t *testing.T, status int, answer []byte, header http.Header) {
				assertTooManyRequests(t, status, answer, header, "1")
			},
		},
		{
			name:   "a caller its counter could not answer about",
			caller: brokenCaller,
			assert: assertRateUnavailable,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			// No call left for either: the one caller spent it below, and the
			// broken one was never counted at all.
			m := meter(t, budget(time.Second, otherCaller))

			status, answer, header := exchange(t, m.srv, asking(t, m.srv, "/things", tc.caller))

			tc.assert(t, status, answer, header)
			if m.things.ran.Load() {
				t.Error("the metered handler ran on a request the wrap refused")
			}
			if got := m.counter.calls.Load(); got != 1 {
				t.Errorf("counter asked %d times, want 1", got)
			}
		})
	}
}

// TestRateRefusesTheSameWhateverCallerAndDelay puts two refused callers side
// by side: what tells their answers apart is the moment each may come back,
// and the body they read is one and the same, so probing a service one caller
// at a time says nothing of anyone else's budget.
func TestRateRefusesTheSameWhateverCallerAndDelay(t *testing.T) {
	t.Parallel()

	c := &counter{
		left:  map[callerKey]int{},
		delay: map[callerKey]time.Duration{oneCaller: 2 * time.Second, otherCaller: 30 * time.Second},
	}
	m := meter(t, c)

	oneStatus, oneAnswer, oneHeader := exchange(t, m.srv, asking(t, m.srv, "/things", oneCaller))
	assertTooManyRequests(t, oneStatus, oneAnswer, oneHeader, "2")

	otherStatus, otherAnswer, otherHeader := exchange(t, m.srv, asking(t, m.srv, "/things", otherCaller))
	assertTooManyRequests(t, otherStatus, otherAnswer, otherHeader, "30")

	if string(oneAnswer) != string(otherAnswer) {
		t.Errorf("answers differ: %s and %s", oneAnswer, otherAnswer)
	}
	if m.things.ran.Load() {
		t.Error("the metered handler ran on a request the wrap refused")
	}
}

// TestRateNamesADelayOfWholeSecondsNotBelowOne pins what a refused caller is
// told to wait: a whole number of seconds, rounded up so that coming back on
// it finds the budget it was promised, and never below one, a zero being an
// invitation to come back at once.
func TestRateNamesADelayOfWholeSecondsNotBelowOne(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		delay time.Duration
		want  string
	}{
		{name: "no delay at all", delay: 0, want: "1"},
		{name: "a delay already past", delay: -5 * time.Second, want: "1"},
		{name: "part of a second", delay: 200 * time.Millisecond, want: "1"},
		{name: "one whole second", delay: time.Second, want: "1"},
		{name: "a second and a sliver", delay: time.Second + time.Millisecond, want: "2"},
		{name: "a second and a half", delay: 1500 * time.Millisecond, want: "2"},
		{name: "half a minute", delay: 30 * time.Second, want: "30"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			m := meter(t, budget(tc.delay, oneCaller))

			status, answer := send(t, m.srv, asking(t, m.srv, "/things", oneCaller))
			assertServed(t, status, answer, "things")

			_, _, header := exchange(t, m.srv, asking(t, m.srv, "/things", oneCaller))

			assertHeader(t, header, "Retry-After", tc.want)
		})
	}
}

// TestRateServesACallerItKeysToNobodyWithNothingCounted pins the cost of the
// keying function being the service's: a request it names no caller for is
// served without a call being taken, so a route reachable with no credential
// and keyed on its principal is metered for nobody.
func TestRateServesACallerItKeysToNobodyWithNothingCounted(t *testing.T) {
	t.Parallel()

	m := meter(t, budget(time.Minute, oneCaller))

	status, answer := send(t, m.srv, asking(t, m.srv, "/things", ""))

	assertServed(t, status, answer, "things")
	if got := m.counter.calls.Load(); got != 0 {
		t.Errorf("counter asked %d times about a request keyed to nobody, want 0", got)
	}
}

// TestRateLeavesARouteItDoesNotCoverServingItsCaller pins coverage as exactly
// the set of handlers wrapped: the caller refused on the route above is served
// by the route beside it, and nothing of its budget is touched there.
func TestRateLeavesARouteItDoesNotCoverServingItsCaller(t *testing.T) {
	t.Parallel()

	m := meter(t, &counter{left: map[callerKey]int{}, delay: map[callerKey]time.Duration{}})

	status, answer := send(t, m.srv, asking(t, m.srv, "/status", oneCaller))

	assertServed(t, status, answer, "status")
	if got := m.counter.calls.Load(); got != 0 {
		t.Errorf("counter asked %d times on a route the wrap does not cover, want 0", got)
	}
}

// TestRateIsAskedNothingBeforeTheTableAnswered pins the order the wrap sits
// in: the table answers first, so no flood of requests nobody routed spends a
// real caller's budget, and the preflight a browser needs is not a request a
// budget can refuse.
func TestRateIsAskedNothingBeforeTheTableAnswered(t *testing.T) {
	t.Parallel()

	t.Run("a path nobody named is still answered 404", func(t *testing.T) {
		t.Parallel()

		m := meter(t, budget(time.Minute, oneCaller))

		status, answer := send(t, m.srv, asking(t, m.srv, "/secret-path", oneCaller))

		assertRefused(t, status, answer)
		if got := m.counter.calls.Load(); got != 0 {
			t.Errorf("counter asked %d times on a path nobody named, want 0", got)
		}
	})

	t.Run("a preflight the table answers is still answered 204", func(t *testing.T) {
		t.Parallel()

		c := &counter{left: map[callerKey]int{}, delay: map[callerKey]time.Duration{}}
		grant, err := httpd.GrantOrigins(listedOrigins, nil)
		if err != nil {
			t.Fatalf("GrantOrigins: %v", err)
		}
		things := &marker{name: "things"}
		table, err := grant.Router(httpd.Route{
			Method:  http.MethodGet,
			Pattern: "/things",
			Handler: httpd.LimitRate(callerOf, c.count)(things),
		})
		if err != nil {
			t.Fatalf("Router: %v", err)
		}
		srv := httptest.NewServer(grant.Wrap(table))
		t.Cleanup(srv.Close)

		req := withCaller(preflighting(t, srv, listedOrigin, "/things", http.MethodGet), oneCaller)
		status, answer, header := exchange(t, srv, req)

		assertPreflight(t, status, answer, header, http.MethodGet, "", listedOrigin)
		if things.ran.Load() {
			t.Error("the metered handler ran on a preflight")
		}
		if got := c.calls.Load(); got != 0 {
			t.Errorf("counter asked %d times on a preflight, want 0", got)
		}
	})
}
