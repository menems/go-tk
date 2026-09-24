package postgres_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/menems/go-tk/storage/postgres"
)

func TestClassifyNil(t *testing.T) {
	t.Parallel()

	if err := postgres.Classify(nil); err != nil {
		t.Fatalf("Classify(nil) = %v, want nil", err)
	}
}

func TestClassifyNoRows(t *testing.T) {
	t.Parallel()

	err := postgres.Classify(failWith(t, newPool(t), queryNoRow))

	if !errors.Is(err, postgres.ErrNoRows) {
		t.Errorf("errors.Is(%v, ErrNoRows) = false, want true", err)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		t.Errorf("errors.Is(%v, pgx.ErrNoRows) = false, want true", err)
	}
	if errors.Is(err, postgres.ErrUniqueViolation) {
		t.Errorf("errors.Is(%v, ErrUniqueViolation) = true, want false", err)
	}
	var failure *postgres.Error
	if errors.As(err, &failure) {
		t.Errorf("errors.As(%v, *Error) = true, want false", err)
	}
	if got := postgres.ConstraintName(err); got != "" {
		t.Errorf("ConstraintName = %q, want empty", got)
	}
}

func TestClassifyUniqueViolation(t *testing.T) {
	t.Parallel()

	err := postgres.Classify(failWith(t, newPool(t), queryCollide))

	if !errors.Is(err, postgres.ErrUniqueViolation) {
		t.Errorf("errors.Is(%v, ErrUniqueViolation) = false, want true", err)
	}
	if errors.Is(err, postgres.ErrNoRows) {
		t.Errorf("errors.Is(%v, ErrNoRows) = true, want false", err)
	}
	var failure *postgres.Error
	if errors.As(err, &failure) {
		t.Errorf("errors.As(%v, *Error) = true, want false", err)
	}
	if got := postgres.ConstraintName(err); got != collisionIndex {
		t.Errorf("ConstraintName = %q, want %q", got, collisionIndex)
	}
	if strings.Contains(err.Error(), "ada@example.com") {
		t.Errorf("message %q carries the backend's detail", err.Error())
	}
}

func TestClassifyFailure(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		query string
		code  string
	}{
		{name: "foreign key violation", query: queryForeign, code: "23503"},
		{name: "serialization failure", query: querySerialize, code: "40001"},
		{name: "query cancelled by the server", query: queryTimedOut, code: "57014"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := postgres.Classify(failWith(t, newPool(t), tt.query))

			assertFailure(t, err)
			var pgErr *pgconn.PgError
			if !errors.As(err, &pgErr) {
				t.Fatalf("errors.As(%v, *pgconn.PgError) = false, want true", err)
			}
			if pgErr.Code != tt.code {
				t.Errorf("Code = %q, want %q", pgErr.Code, tt.code)
			}
		})
	}

	t.Run("context cancelled before the call", func(t *testing.T) {
		t.Parallel()

		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, cause := newPool(t).Exec(ctx, queryForeign)

		err := postgres.Classify(cause)

		assertFailure(t, err)
		if !errors.Is(err, context.Canceled) {
			t.Errorf("errors.Is(%v, context.Canceled) = false, want true", err)
		}
	})

	t.Run("backend closes the connection", func(t *testing.T) {
		t.Parallel()

		err := postgres.Classify(failWith(t, newPool(t), queryClose))

		assertFailure(t, err)
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) {
			t.Errorf("errors.As(%v, *pgconn.PgError) = true, want false", err)
		}
	})
}

func assertFailure(t *testing.T, err error) {
	t.Helper()

	var failure *postgres.Error
	if !errors.As(err, &failure) {
		t.Errorf("errors.As(%v, *Error) = false, want true", err)
	}
	if errors.Is(err, postgres.ErrNoRows) {
		t.Errorf("errors.Is(%v, ErrNoRows) = true, want false", err)
	}
	if errors.Is(err, postgres.ErrUniqueViolation) {
		t.Errorf("errors.Is(%v, ErrUniqueViolation) = true, want false", err)
	}
	if got := postgres.ConstraintName(err); got != "" {
		t.Errorf("ConstraintName = %q, want empty", got)
	}
}

func TestClassifyAlreadyRead(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		query string
	}{
		{name: "missing row", query: queryNoRow},
		{name: "unique violation", query: queryCollide},
		{name: "failure", query: queryForeign},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			read := postgres.Classify(failWith(t, newPool(t), tt.query))
			wrapped := fmt.Errorf("create user: %w", read)

			if got := postgres.Classify(read); got != read {
				t.Errorf("Classify(read) = %v, want it unchanged: %v", got, read)
			}
			if got := postgres.Classify(wrapped); got != wrapped {
				t.Errorf("Classify(wrapped) = %v, want it unchanged: %v", got, wrapped)
			}
		})
	}

	t.Run("unique violation returned through BeginFunc", func(t *testing.T) {
		t.Parallel()

		pool := newPool(t)
		var read error
		cause := pgx.BeginFunc(context.Background(), pool, func(tx pgx.Tx) error {
			_, err := tx.Exec(context.Background(), queryCollide)
			read = postgres.Classify(err)
			return read
		})

		err := postgres.Classify(cause)

		if err != read {
			t.Errorf("Classify = %v, want the collision read in the callback: %v", err, read)
		}
		if got := postgres.ConstraintName(err); got != collisionIndex {
			t.Errorf("ConstraintName = %q, want %q", got, collisionIndex)
		}
	})
}

// sentValue stands for a value a statement sent, which a server field may quote.
const sentValue = "ada@example.com"

func TestClassifyFailureText(t *testing.T) {
	t.Parallel()

	refusal := &pgconn.PgError{
		Severity:            "ERROR",
		SeverityUnlocalized: "ERROR",
		Code:                "22P02",
		Message:             `invalid input syntax for type integer: "` + sentValue + `"`,
		Detail:              "Value " + sentValue + ".",
		Hint:                "Check " + sentValue + ".",
		Where:               "column " + sentValue,
		InternalQuery:       "select " + sentValue,
		SchemaName:          sentValue,
		TableName:           sentValue,
		ColumnName:          sentValue,
		DataTypeName:        sentValue,
		ConstraintName:      sentValue,
		File:                sentValue,
		Routine:             sentValue,
	}

	tests := []struct {
		name  string
		cause error
		want  string
	}{
		{name: "server refusal", cause: refusal, want: "pg: server error, SQLSTATE 22P02"},
		{name: "server refusal wrapped", cause: fmt.Errorf("exec %s: %w", sentValue, refusal), want: "pg: server error, SQLSTATE 22P02"},
		{name: "network failure", cause: errors.New("dial tcp 127.0.0.1:5432: connection refused"), want: "pg: dial tcp 127.0.0.1:5432: connection refused"},
		{name: "context cancelled", cause: context.Canceled, want: "pg: context canceled"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := postgres.Classify(tt.cause)

			assertFailure(t, err)
			if got := err.Error(); got != tt.want {
				t.Errorf("Error() = %q, want %q", got, tt.want)
			}
			if !errors.Is(err, tt.cause) {
				t.Errorf("errors.Is(%v, cause) = false, want true", err)
			}
		})
	}

	t.Run("server refusal reachable whole", func(t *testing.T) {
		t.Parallel()

		err := postgres.Classify(fmt.Errorf("exec: %w", refusal))

		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) {
			t.Fatalf("errors.As(%v, *pgconn.PgError) = false, want true", err)
		}
		if pgErr != refusal {
			t.Errorf("errors.As yielded %+v, want the server's refusal %+v", pgErr, refusal)
		}
	})
}
