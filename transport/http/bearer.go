package httpd

import (
	"context"
	"net/http"

	"github.com/menems/go-tk/authctx"
)

// CodeUnauthenticated is what a covered route refuses with: the request
// carries no bearer credential this service resolves to a principal.
const CodeUnauthenticated ErrorCode = "unauthenticated"

// Fixed, like the rest of the envelope's messages, and it describes the
// request rather than quoting it: neither the token, nor the scheme that was
// read, nor what the resolver answered about either.
const messageUnauthenticated = "this request carries no credential this service accepts"

// The challenge a 401 owes the client: the scheme to retry under, and no
// realm, which names a service rather than anything this package knows.
const challengeBearer = "Bearer"

// Resolver turns the bearer token of a request into the principal it belongs
// to, and refuses the request with an error.
//
// It is the service's own: the algorithm, the key material and the store are
// exactly where services differ, which is why nothing here verifies anything.
// ctx is the request's, so a lookup it makes is cancelled with the request
// that asked for it.
type Resolver[T any] func(ctx context.Context, token string) (T, error)

// RequireBearer covers the handlers of the routes a service authenticates: the
// bearer token of the request reaches resolve, the principal it answers rides
// in the request context under key, and the handler reads it back with
// key.From.
//
//	var userID = authctx.NewKey[uuid.UUID]("user_id")
//
//	auth := httpd.RequireBearer(userID, verify) // verify is yours
//	h, err := httpd.NewRouter(
//	    httpd.Route{Method: http.MethodGet, Pattern: "/things", Handler: auth(list)},
//	    httpd.Route{Method: http.MethodGet, Pattern: "/status", Handler: status},
//	)
//
// A request carrying no bearer credential, or one resolve refuses, is answered
// 401 under CodeUnauthenticated with a WWW-Authenticate header, and the
// wrapped handler does not run. Nothing of the credential, and nothing resolve
// said about it, reaches that answer.
//
// The wrap sits on the handler rather than in front of the table, so coverage
// is exactly the set of handlers wrapped and is named where the route is. A
// route left uncovered serves with no principal in its context, and a handler
// finding none has to treat that as unauthenticated. It is also what keeps a
// request no route matched away from resolve: the table answers its 404 and
// its 405 first, so enumerating paths and methods stays anonymous.
func RequireBearer[T any](key *authctx.Key[T], resolve Resolver[T]) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			token, ok := authctx.Bearer(r.Header)
			if !ok {
				refuseUnauthenticated(w)
				return
			}

			principal, err := resolve(r.Context(), token)
			if err != nil {
				// The text is the resolver's own and may name the credential
				// it read, so it reaches neither this answer nor a log here.
				refuseUnauthenticated(w)
				return
			}

			next.ServeHTTP(w, r.WithContext(key.With(r.Context(), principal)))
		})
	}
}

// refuseUnauthenticated writes the one answer a covered route gives a request
// it could not authenticate.
func refuseUnauthenticated(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate", challengeBearer)
	WriteError(w, http.StatusUnauthorized, CodeUnauthenticated, messageUnauthenticated)
}
