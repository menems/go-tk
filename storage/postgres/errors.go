package postgres

import (
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// ErrNoRows is what Classify returns for a single-row query the server
// answered with no row. errors.Is also matches it to pgx.ErrNoRows, so code
// already testing pgx's sentinel keeps matching.
var ErrNoRows = fmt.Errorf("pg: %w", pgx.ErrNoRows)

// ErrUniqueViolation is matched by errors.Is to what Classify returns for a
// statement the server refused under SQLSTATE 23505. ConstraintName reads the
// index it hit.
var ErrUniqueViolation = errors.New("pg: unique violation")

// uniqueViolationCode is SQLSTATE unique_violation.
const uniqueViolationCode = "23505"

// uniqueViolation keeps the constraint name and drops the server's detail,
// which quotes the colliding row's values.
type uniqueViolation struct {
	constraint string
}

func (uniqueViolation) Error() string { return ErrUniqueViolation.Error() }

func (uniqueViolation) Is(target error) bool { return target == ErrUniqueViolation }

// Error is what Classify returns for every failure that is neither a missing
// row nor a unique violation. Err is the driver's error: errors.As to
// *pgconn.PgError and errors.Is to a context error reach it through Unwrap.
//
// When Err's chain holds a *pgconn.PgError, the message is fixed and names
// only its SQLSTATE: "pg: server error, SQLSTATE 22P02". The server's own
// fields, and the text of whatever wraps them, are left out, since they can
// quote a value the statement sent (an invalid input syntax does). Any other
// cause keeps its own text: "pg: " + Err's. That covers network, context and
// pool failures, and also the errors pgx builds client-side: an encode failure
// quotes the value sent ("unable to encode <value> into ... format"), so the
// text is no safer than the argument that failed.
type Error struct {
	Err error
}

func (e *Error) Error() string {
	var pgErr *pgconn.PgError
	if errors.As(e.Err, &pgErr) {
		return "pg: server error, SQLSTATE " + pgErr.Code
	}
	return fmt.Sprintf("pg: %v", e.Err)
}

func (e *Error) Unwrap() error { return e.Err }

// Classify reads an error pgx returned, before the service maps it to its own
// kinds: nil stays nil, a missing row is ErrNoRows, a unique violation matches
// ErrUniqueViolation, and every other error comes back as *Error. An error
// whose chain already holds one of these three comes back unchanged, wrapped
// or not.
//
// The rule is positional: Classify cannot tell a driver failure from an error
// a callback handed back through pgx.BeginFunc, and takes both as the
// driver's. A service reads the errors inside its transaction callback and
// returns what Classify gave it.
func Classify(err error) error {
	if err == nil {
		return nil
	}

	var failure *Error
	if errors.Is(err, ErrNoRows) || errors.Is(err, ErrUniqueViolation) || errors.As(err, &failure) {
		return err
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNoRows
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == uniqueViolationCode {
		return uniqueViolation{constraint: pgErr.ConstraintName}
	}
	return &Error{Err: err}
}

// ConstraintName returns the name of the unique index a violation read by
// Classify hit, and an empty name for any other error.
func ConstraintName(err error) string {
	var v uniqueViolation
	if errors.As(err, &v) {
		return v.constraint
	}
	return ""
}
