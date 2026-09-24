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
