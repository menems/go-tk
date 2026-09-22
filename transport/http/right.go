package httpd

import (
	"context"
	"net/http"

	"github.com/menems/go-tk/authctx"
)

// CodeForbidden is the code a route demanding a right refuses with: the caller
// of this request was judged, and holds that right nowhere. A request whose
// right nobody could judge is answered under CodeAuthUnavailable instead.
const CodeForbidden ErrorCode = "forbidden"

// Fixed, like the rest of the envelope's messages, and they name neither the
// right demanded nor any right the caller does hold: the first tells a caller
// which right to go and get, the second answers a question it never asked.
// messageRightUnavailable names no check and quotes nothing it said, for the
// same reason messageAuthUnavailable does not.
const (
	messageForbidden        = "this credential does not allow this request"
	messageRightUnavailable = "this service could not answer whether this request is allowed"
)

// Holds answers whether principal holds right.
//
// The error says which of two things happened. A nil error is a verdict and
// the bool carries it: true serves the request, false refuses it 403. Any
// other error is the check failing rather than judging, the bool is not read,
// and the request is answered 500, that being the same trade Resolver makes
// and for the same reason: a right store that is down reported as a caller
// that was turned away hides the outage behind the one status nobody
// investigates, and leaves an operator unable to tell a route closed on
// purpose from one closed by a wiring mistake. Both answers deny the request,
// so what the two tell apart is which of them an operator has to go and fix.
//
// It is the service's own: the right table, its shape and where it lives are
// exactly where services differ, which is why nothing here reads right. A
// holds that answers wrongly authorizes wrongly.
//
// ctx is the request's, so a lookup it makes is cancelled with the request
// that asked for it.
type Holds[T, R any] func(ctx context.Context, principal T, right R) (bool, error)

// RequireRight covers the handler of a route a service closes on a right: the
// principal riding in the request context under key reaches holds with right,
// and the handler runs only when holds answers true with no error.
//
//	var userID = authctx.NewKey[uuid.UUID]("user_id")
//
//	auth := httpd.RequireBearer(userID, verify)                 // verify is yours
//	listing := httpd.RequireRight(userID, scopeList, allowed)   // allowed is yours
//	h, err := httpd.NewRouter(
//	    httpd.Route{Method: http.MethodGet, Pattern: "/things", Handler: auth(listing(list))},
//	    httpd.Route{Method: http.MethodGet, Pattern: "/me", Handler: auth(me)},
//	)
//
// A request whose caller holds that right nowhere is answered 403 under
// CodeForbidden. One whose holds failed instead of judging is answered 500
// under CodeAuthUnavailable, and so is one reaching this wrap with no
// principal in its context, which is a route wired without RequireBearer above
// it: nothing was judged there either, and a 403 would tell a caller it lacks
// a right when what it met was a mistake in the table. The wrapped handler
// runs in none of the three.
//
// Both answers are one fixed body under one code, the same whatever right was
// demanded and whatever the caller does hold, and neither carries the
// principal, the right, or anything holds said. Neither carries a
// WWW-Authenticate header either: a challenge invites a retry, and no
// credential this caller can present makes this request go through.
//
// This package holds no right table and grants nothing. The verdict is the
// service's own, which is why nothing here reads right.
//
// The wrap sits on the handler and inside the bearer wrap, so the order a
// request meets is the route table, then the bearer, then the right. Nothing
// is asked about rights for a request no route matched or no credential
// authenticated, and coverage is exactly the set of handlers wrapped: a route
// wrapped for no right demands none. A route named for GET is reached by HEAD
// too, and the wrap sitting on the handler, that HEAD is checked like the GET.
func RequireRight[T, R any](key *authctx.Key[T], right R, holds Holds[T, R]) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			principal, ok := key.From(r.Context())
			if !ok {
				// Nothing to ask holds about, so no verdict exists to report.
				// Serving here would make a route left outside RequireBearer
				// an open door.
				refuseRightUnavailable(w)
				return
			}

			allowed, err := holds(r.Context(), principal, right)
			if err != nil {
				// The text is the check's own and may name the principal it
				// was asked about or the store it read, so it reaches neither
				// the answer nor a log here: what an operator has to go on is
				// the status and the code.
				refuseRightUnavailable(w)
				return
			}
			if !allowed {
				WriteError(w, http.StatusForbidden, CodeForbidden, messageForbidden)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

// refuseRightUnavailable writes the one answer a route demanding a right gives
// a request whose right nobody judged. One function for the two branches, so
// they cannot drift into two answers a caller could tell apart.
func refuseRightUnavailable(w http.ResponseWriter) {
	WriteError(w, http.StatusInternalServerError, CodeAuthUnavailable, messageRightUnavailable)
}
