package httpd_test

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/menems/go-tk/transport/http"
)

// trail records what a request met on its way in and on its way out, one
// entry at a time. The server serves on goroutines of its own, so it is
// written under a lock.
type trail struct {
	mu  sync.Mutex
	met []string
}

func (tr *trail) note(s string) {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	tr.met = append(tr.met, s)
}

func (tr *trail) read() []string {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	return slices.Clone(tr.met)
}

// recording is a wrap of the test's own, in the unnamed type a service writes
// its wraps in: it notes its name before and after the handler it wraps.
func (tr *trail) recording(name string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			tr.note(name + " in")
			next.ServeHTTP(w, r)
			tr.note(name + " out")
		})
	}
}

func TestChainRunsTheWrapsInTheOrderListedTheFirstOutermost(t *testing.T) {
	t.Parallel()

	tr := &trail{}
	srv := gate(t, httpd.Route{
		Method:  http.MethodGet,
		Pattern: "/things",
		Handler: httpd.Chain(tr.recording("first"), tr.recording("second"), tr.recording("third"))(
			http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				tr.note("handler")
				httpd.WriteJSON(w, http.StatusOK, payload{Name: "things"})
			})),
	})

	status, answer := send(t, srv, ask(t, srv, http.MethodGet, "/things", nil))

	assertServed(t, status, answer, "things")
	want := []string{"first in", "second in", "third in", "handler", "third out", "second out", "first out"}
	if got := tr.read(); !slices.Equal(got, want) {
		t.Errorf("met %q, want %q", got, want)
	}
}

// side is one table of two routes: one whose handler carries the bearer, the
// right and the rate, and one carrying none, plus the resolver the bearer was
// given. Each side has its own resolver, holder and counter, so what a
// request spends on one the other does not see.
type side struct {
	srv      *httptest.Server
	covered  *reader
	open     *reader
	resolver *resolver
}

// covering is the same table built twice: once with the three wraps composed
// by Chain, once with them nested by hand.
type covering struct{ chained, nested side }

// cover builds that pair, granting rightList when held is true, one call to
// oneCaller and none to otherCaller. This is the seam: a consumer meets the
// chain through the handler NewRouter returns, one served request at a time.
func cover(t *testing.T, held bool) covering {
	t.Helper()

	build := func(compose func(rv *resolver, h *holder, c *counter) func(http.Handler) http.Handler) side {
		rv := &resolver{}
		wrap := compose(rv, &holder{held: map[right]bool{rightList: held}}, budget(2*time.Second, oneCaller))
		covered, open := &reader{}, &reader{}
		srv := gate(t,
			httpd.Route{Method: http.MethodGet, Pattern: "/things", Handler: wrap(covered)},
			httpd.Route{Method: http.MethodGet, Pattern: "/status", Handler: open},
		)
		return side{srv: srv, covered: covered, open: open, resolver: rv}
	}

	return covering{
		chained: build(func(rv *resolver, h *holder, c *counter) func(http.Handler) http.Handler {
			// Handed to Chain as they are returned, with no conversion.
			return httpd.Chain(
				httpd.RequireBearer(principalKey, rv.resolve),
				httpd.RequireRight(principalKey, rightList, h.holds),
				httpd.LimitRate(callerOf, c.count),
			)
		}),
		nested: build(func(rv *resolver, h *holder, c *counter) func(http.Handler) http.Handler {
			auth := httpd.RequireBearer(principalKey, rv.resolve)
			listing := httpd.RequireRight(principalKey, rightList, h.holds)
			meter := httpd.LimitRate(callerOf, c.count)
			return func(next http.Handler) http.Handler { return auth(listing(meter(next))) }
		}),
	}
}

func TestChainAnswersExactlyWhatTheNestedWrapsAnswered(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		held       bool
		bearer     string
		caller     callerKey
		wantStatus int
		wantRan    bool
	}{
		{name: "a caller accepted, holding the right, inside its cadence", held: true, bearer: "Bearer " + goodToken, caller: oneCaller, wantStatus: http.StatusOK, wantRan: true},
		{name: "a request carrying no credential", held: true, bearer: "", caller: oneCaller, wantStatus: http.StatusUnauthorized},
		{name: "a caller holding the right nowhere", held: false, bearer: "Bearer " + goodToken, caller: oneCaller, wantStatus: http.StatusForbidden},
		{name: "a caller past its cadence", held: true, bearer: "Bearer " + goodToken, caller: otherCaller, wantStatus: http.StatusTooManyRequests},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			c := cover(t, tc.held)

			chainedStatus, chainedAnswer, chainedHeader := exchange(t, c.chained.srv,
				withCaller(withBearer(ask(t, c.chained.srv, http.MethodGet, "/things", nil), tc.bearer), tc.caller))
			nestedStatus, nestedAnswer, nestedHeader := exchange(t, c.nested.srv,
				withCaller(withBearer(ask(t, c.nested.srv, http.MethodGet, "/things", nil), tc.bearer), tc.caller))

			if chainedStatus != tc.wantStatus {
				t.Errorf("status = %d, want %d (%s)", chainedStatus, tc.wantStatus, chainedAnswer)
			}
			if chainedStatus != nestedStatus || string(chainedAnswer) != string(nestedAnswer) {
				t.Errorf("chained answered %d %s, nested %d %s", chainedStatus, chainedAnswer, nestedStatus, nestedAnswer)
			}
			if got, want := withoutDate(chainedHeader), withoutDate(nestedHeader); !equalHeaders(got, want) {
				t.Errorf("chained headers %q, nested %q", got, want)
			}
			if got := c.chained.covered.ran.Load(); got != tc.wantRan {
				t.Errorf("chained handler ran = %t, want %t", got, tc.wantRan)
			}
			if got := c.nested.covered.ran.Load(); got != tc.wantRan {
				t.Errorf("nested handler ran = %t, want %t", got, tc.wantRan)
			}
		})
	}
}

// equalHeaders compares two headers name by name, each name's values in the
// order they were written.
func equalHeaders(a, b http.Header) bool {
	if len(a) != len(b) {
		return false
	}
	for name, values := range a {
		if !slices.Equal(values, b[name]) {
			return false
		}
	}
	return true
}

func TestChainCoversOnlyTheHandlerItIsSetOn(t *testing.T) {
	t.Parallel()

	t.Run("a route of the same table carrying no chain serves with no resolver asked", func(t *testing.T) {
		t.Parallel()

		c := cover(t, true).chained

		status, answer := send(t, c.srv, ask(t, c.srv, http.MethodGet, "/status", nil))

		assertServed(t, status, answer, "anonymous")
		if !c.open.ran.Load() {
			t.Error("the uncovered handler did not run")
		}
		if got := c.resolver.calls.Load(); got != 0 {
			t.Errorf("resolver asked %d times on a route carrying no chain, want 0", got)
		}
	})

	t.Run("a path nobody named is answered 404 with the resolver asked nothing", func(t *testing.T) {
		t.Parallel()

		c := cover(t, true).chained

		req := withCaller(withBearer(ask(t, c.srv, http.MethodGet, "/secret-path", nil), "Bearer "+goodToken), oneCaller)
		status, answer := send(t, c.srv, req)

		assertRefused(t, status, answer)
		if got := c.resolver.calls.Load(); got != 0 {
			t.Errorf("resolver asked %d times on a path nobody named, want 0", got)
		}
	})
}

func TestChainOfNoWrapServesTheHandlerAsItIs(t *testing.T) {
	t.Parallel()

	things := &marker{name: "things"}
	srv := gate(t, httpd.Route{Method: http.MethodGet, Pattern: "/things", Handler: httpd.Chain()(things)})

	status, answer := send(t, srv, ask(t, srv, http.MethodGet, "/things", nil))

	assertServed(t, status, answer, "things")
	if !things.ran.Load() {
		t.Error("the handler did not run")
	}
}

func TestChainLeavesAWrapAssignableToTheUnnamedType(t *testing.T) {
	t.Parallel()

	rv := &resolver{}
	covered := &reader{}
	// The shape RequireBearer returned before Middleware was named: a service
	// holding its wraps in such a variable still compiles and still covers.
	var auth func(http.Handler) http.Handler = httpd.RequireBearer(principalKey, rv.resolve)
	srv := gate(t, httpd.Route{Method: http.MethodGet, Pattern: "/things", Handler: auth(covered)})

	status, answer, header := exchange(t, srv, ask(t, srv, http.MethodGet, "/things", nil))

	assertUnauthenticated(t, status, answer, header)
	if covered.ran.Load() {
		t.Error("the covered handler ran on a request carrying no credential")
	}
}

func TestChainPanicsAtWiringOnANilWrap(t *testing.T) {
	t.Parallel()

	tr := &trail{}
	cases := []struct {
		name  string
		wraps []httpd.Middleware
	}{
		{name: "a nil wrap alone", wraps: []httpd.Middleware{nil}},
		{name: "a nil wrap after a wrap", wraps: []httpd.Middleware{tr.recording("first"), nil}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			defer func() {
				if recover() == nil {
					t.Error("Chain composed a nil wrap without panicking")
				}
			}()
			// Composed and never set on a handler, nor served: the panic is
			// owed here, before any request exists.
			httpd.Chain(tc.wraps...)
		})
	}
}
