package httpd_test

import (
	"context"
	"errors"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync/atomic"
	"testing"

	"github.com/menems/go-tk/transport/http"
)

// right stands for whatever a service calls the thing a caller must hold: a
// scope of a token, a permission of a role, a row of its own table. It is the
// service's type, and this package never reads it.
type right string

// The rights the table below demands, a third the caller holds while being
// refused, and a fourth the check cannot answer about at all. All four carry a
// marker, so an answer repeating any of them is caught by the no-echo
// assertions.
const (
	rightList   right = "marker-things-list"
	rightPurge  right = "marker-things-purge"
	rightHeld   right = "marker-things-else"
	rightBroken right = "marker-things-broken"
)

// The two refusals a route demanding a right gives, written here as the
// literals a client reads.
const (
	messageForbidden        = "this credential does not allow this request"
	messageRightUnavailable = "this service could not answer whether this request is allowed"
)

// The text the holder puts in its own error, and what the no-echo assertions
// look for in the answer that request gets: the check's own innards.
const checkFailureText = "marker-right-store unreachable"

// holder stands for the service's own right table: it answers whether the
// principal it is handed holds the right the route demanded, and answers one
// right with a failure of its own instead of a verdict. It records how many
// times it was asked anything, so a request that must reach no check can be
// checked to have reached none.
//
// held is written at construction and only read afterwards, so the parallel
// tests below share nothing through it.
type holder struct {
	held  map[right]bool
	calls atomic.Int64
}

func (h *holder) holds(_ context.Context, _ principal, r right) (bool, error) {
	h.calls.Add(1)
	if r == rightBroken {
		// Not a verdict: this stands for the right store being down or its
		// rows being unreadable, neither of which is the caller's fault.
		return false, errors.New(checkFailureText)
	}
	return h.held[r], nil
}

// demanding is one table of four covered routes: three demanding three
// different rights, one demanding none, each with its own handler, plus the
// holder the three demands were given.
type demanding struct {
	srv    *httptest.Server
	holder *holder
	list   *reader
	purge  *reader
	broken *reader
	open   *reader
}

// demand builds that table, granting the caller exactly the rights named. This
// is the seam: a consumer meets the wrap through the handler NewRouter
// returns, one served request at a time.
//
// The order the wraps are written in is the order a request meets them: the
// table, then the bearer, then the right.
func demand(t *testing.T, held ...right) demanding {
	t.Helper()

	h := &holder{held: make(map[right]bool, len(held))}
	for _, r := range held {
		h.held[r] = true
	}
	auth := httpd.RequireBearer(principalKey, (&resolver{}).resolve)
	list, purge, broken, open := &reader{}, &reader{}, &reader{}, &reader{}
	srv := gate(t,
		httpd.Route{
			Method:  http.MethodGet,
			Pattern: "/things",
			Handler: auth(httpd.RequireRight(principalKey, rightList, h.holds)(list)),
		},
		httpd.Route{
			Method:  http.MethodDelete,
			Pattern: "/things",
			Handler: auth(httpd.RequireRight(principalKey, rightPurge, h.holds)(purge)),
		},
		httpd.Route{
			Method:  http.MethodPost,
			Pattern: "/things",
			Handler: auth(httpd.RequireRight(principalKey, rightBroken, h.holds)(broken)),
		},
		httpd.Route{Method: http.MethodGet, Pattern: "/status", Handler: auth(open)},
	)

	return demanding{srv: srv, holder: h, list: list, purge: purge, broken: broken, open: open}
}

// assertSaysNothingOfTheCheck checks an answer names none of the rights in
// play, nothing of the principal, and nothing the check itself said.
func assertSaysNothingOfTheCheck(t *testing.T, answer []byte) {
	t.Helper()

	for _, r := range []right{rightList, rightPurge, rightHeld, rightBroken} {
		assertNoEcho(t, answer, string(r))
	}
	assertNoEcho(t, answer, "owner")
	assertNoEcho(t, answer, checkFailureText)
}

// assertForbidden checks the answer is the wrap's 403 envelope, says nothing
// of the check, and carries no challenge: a scheme to retry under would invite
// a retry there is no credential for.
func assertForbidden(t *testing.T, status int, answer []byte, header http.Header) {
	t.Helper()

	assertRefusal(t, status, answer, http.StatusForbidden, httpd.CodeForbidden, messageForbidden)
	assertSaysNothingOfTheCheck(t, answer)
	assertNoChallenge(t, header)
}

// assertRightUnavailable checks the answer is the wrap's 500 envelope, under
// the code this package already writes for a verdict it could not reach, and
// says nothing of the check either.
func assertRightUnavailable(t *testing.T, status int, answer []byte, header http.Header) {
	t.Helper()

	assertRefusal(t, status, answer, http.StatusInternalServerError, httpd.CodeAuthUnavailable, messageRightUnavailable)
	assertSaysNothingOfTheCheck(t, answer)
	assertNoChallenge(t, header)
}

func assertNoChallenge(t *testing.T, header http.Header) {
	t.Helper()

	if got := header.Values("WWW-Authenticate"); len(got) != 0 {
		t.Errorf("WWW-Authenticate = %q, want none on an answer no credential changes", got)
	}
}

// withoutDate drops the one header two answers written at two moments cannot
// share, so what is left is what the wrap itself put on the wire.
func withoutDate(h http.Header) http.Header {
	out := h.Clone()
	out.Del("Date")
	return out
}

func TestRightReachesTheHandlerOfARouteWhoseRightItsCallerHolds(t *testing.T) {
	t.Parallel()

	d := demand(t, rightList)

	req := withBearer(ask(t, d.srv, http.MethodGet, "/things", nil), "Bearer "+goodToken)
	status, answer := send(t, d.srv, req)

	assertServed(t, status, answer, "owner")
	if got := d.holder.calls.Load(); got != 1 {
		t.Errorf("holder asked %d times, want 1", got)
	}
}

func TestRightRefusesACallerHoldingItNowhere(t *testing.T) {
	t.Parallel()

	d := demand(t, rightHeld)

	req := withBearer(ask(t, d.srv, http.MethodGet, "/things", nil), "Bearer "+goodToken)
	status, answer, header := exchange(t, d.srv, req)

	assertForbidden(t, status, answer, header)
	if d.list.ran.Load() {
		t.Error("the covered handler ran on a request the wrap refused")
	}
	if got := d.holder.calls.Load(); got != 1 {
		t.Errorf("holder asked %d times, want 1", got)
	}
}

// TestRightTellsACallerHoldingNothingFromACheckAnsweringNothing puts the two
// answers a right check can get side by side: a client branches on the status
// and the code, and on nothing else, because nothing of the right demanded, of
// the principal, or of what the check said is in either answer. The caller is
// the same one in both rows, so what differs is the check's own answer.
func TestRightTellsACallerHoldingNothingFromACheckAnsweringNothing(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name        string
		method      string
		wantStatus  int
		wantCode    httpd.ErrorCode
		wantMessage string
		handler     func(demanding) *reader
	}{
		{
			name:        "a right its caller holds nowhere",
			method:      http.MethodGet,
			wantStatus:  http.StatusForbidden,
			wantCode:    httpd.CodeForbidden,
			wantMessage: messageForbidden,
			handler:     func(d demanding) *reader { return d.list },
		},
		{
			name:        "a right its check could not answer about",
			method:      http.MethodPost,
			wantStatus:  http.StatusInternalServerError,
			wantCode:    httpd.CodeAuthUnavailable,
			wantMessage: messageRightUnavailable,
			handler:     func(d demanding) *reader { return d.broken },
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			d := demand(t, rightHeld)

			req := withBearer(ask(t, d.srv, tc.method, "/things", nil), "Bearer "+goodToken)
			status, answer, header := exchange(t, d.srv, req)

			assertRefusal(t, status, answer, tc.wantStatus, tc.wantCode, tc.wantMessage)
			assertSaysNothingOfTheCheck(t, answer)
			assertNoChallenge(t, header)
			if tc.handler(d).ran.Load() {
				t.Error("the covered handler ran on a request the wrap did not allow")
			}
			if got := d.holder.calls.Load(); got != 1 {
				t.Errorf("holder asked %d times, want 1", got)
			}
		})
	}
}

// TestRightRefusesTheSameWhateverRightItDemanded puts the two refusals side by
// side: a caller learns from them that it may not make the request, and not
// which right would have let it, so probing a service's table one route at a
// time returns the same answer every time.
func TestRightRefusesTheSameWhateverRightItDemanded(t *testing.T) {
	t.Parallel()

	d := demand(t, rightHeld)

	listStatus, listAnswer, listHeader := exchange(t, d.srv,
		withBearer(ask(t, d.srv, http.MethodGet, "/things", nil), "Bearer "+goodToken))
	assertForbidden(t, listStatus, listAnswer, listHeader)

	purgeStatus, purgeAnswer, purgeHeader := exchange(t, d.srv,
		withBearer(ask(t, d.srv, http.MethodDelete, "/things", nil), "Bearer "+goodToken))
	assertForbidden(t, purgeStatus, purgeAnswer, purgeHeader)

	if string(listAnswer) != string(purgeAnswer) {
		t.Errorf("answers differ: %s and %s", listAnswer, purgeAnswer)
	}
	if !maps.EqualFunc(withoutDate(listHeader), withoutDate(purgeHeader), slices.Equal) {
		t.Errorf("headers differ: %v and %v", withoutDate(listHeader), withoutDate(purgeHeader))
	}
	if d.list.ran.Load() || d.purge.ran.Load() {
		t.Error("a covered handler ran on a request the wrap refused")
	}
}

// TestRightLeavesARouteDemandingNoneServingItsCaller pins coverage as exactly
// the set of handlers wrapped: the caller refused on the routes above is
// served by the route beside them, whose handler still reads the principal the
// bearer wrap put in its context.
func TestRightLeavesARouteDemandingNoneServingItsCaller(t *testing.T) {
	t.Parallel()

	d := demand(t, rightHeld)

	req := withBearer(ask(t, d.srv, http.MethodGet, "/status", nil), "Bearer "+goodToken)
	status, answer := send(t, d.srv, req)

	assertServed(t, status, answer, "owner")
	if got := d.holder.calls.Load(); got != 0 {
		t.Errorf("holder asked %d times on a route demanding no right, want 0", got)
	}
}

// TestRightIsAskedNothingBeforeTheTableAndTheBearerAnswered pins the order the
// wrap sits in: what a caller can learn about the table is what the router and
// the bearer wrap left it, and neither of their answers turns into a 403.
func TestRightIsAskedNothingBeforeTheTableAndTheBearerAnswered(t *testing.T) {
	t.Parallel()

	t.Run("a path nobody named is still answered 404", func(t *testing.T) {
		t.Parallel()

		d := demand(t, rightList)

		req := withBearer(ask(t, d.srv, http.MethodGet, "/secret-path", nil), "Bearer "+goodToken)
		status, answer := send(t, d.srv, req)

		assertRefused(t, status, answer)
		if got := d.holder.calls.Load(); got != 0 {
			t.Errorf("holder asked %d times on a path nobody named, want 0", got)
		}
	})

	t.Run("a request carrying no credential is still answered 401", func(t *testing.T) {
		t.Parallel()

		d := demand(t, rightList)

		status, answer, header := exchange(t, d.srv, ask(t, d.srv, http.MethodGet, "/things", nil))

		assertUnauthenticated(t, status, answer, header)
		if d.list.ran.Load() {
			t.Error("the covered handler ran on a request the bearer wrap refused")
		}
		if got := d.holder.calls.Load(); got != 0 {
			t.Errorf("holder asked %d times on an unauthenticated request, want 0", got)
		}
	})
}

// TestRightAnswersAWrapLeftWithNoPrincipalAsACheckAnsweringNothing pins the
// wrap failing closed on a route wired without RequireBearer above it: no
// principal reaches it, so no check is asked and no verdict exists, and the
// answer is the one a check answering nothing gets rather than the 403 of a
// caller that was judged. A caller told 403 there would read a wiring mistake
// as a right it lacks, and an operator would read it as a route closed on
// purpose.
func TestRightAnswersAWrapLeftWithNoPrincipalAsACheckAnsweringNothing(t *testing.T) {
	t.Parallel()

	h := &holder{held: map[right]bool{rightList: true}}
	bare := &reader{}
	srv := gate(t, httpd.Route{
		Method:  http.MethodGet,
		Pattern: "/things",
		Handler: httpd.RequireRight(principalKey, rightList, h.holds)(bare),
	})

	req := withBearer(ask(t, srv, http.MethodGet, "/things", nil), "Bearer "+goodToken)
	status, answer, header := exchange(t, srv, req)

	assertRightUnavailable(t, status, answer, header)
	if bare.ran.Load() {
		t.Error("the covered handler ran on a request carrying no principal")
	}
	if got := h.calls.Load(); got != 0 {
		t.Errorf("holder asked %d times with no principal to hand it, want 0", got)
	}

	// The two are one answer, so a caller cannot tell a route left outside the
	// bearer wrap from a right store that is down.
	d := demand(t, rightHeld)
	_, failed, _ := exchange(t, d.srv,
		withBearer(ask(t, d.srv, http.MethodPost, "/things", nil), "Bearer "+goodToken))
	if string(answer) != string(failed) {
		t.Errorf("answers differ: %s and %s", answer, failed)
	}
}
