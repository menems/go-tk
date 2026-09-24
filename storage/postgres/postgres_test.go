package postgres_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/menems/go-tk/storage/postgres"
)

// validDSN points at a port nothing listens on. pgxpool connects lazily, so
// New succeeds and no test here needs a live database.
const validDSN = "postgres://user:pass@127.0.0.1:1/testdb?sslmode=disable"

func TestNewInvalidDSN(t *testing.T) {
	t.Parallel()

	if _, err := postgres.New(context.Background(), "://not-a-dsn"); err == nil {
		t.Fatal("expected an error for a malformed DSN, got nil")
	}
}

func TestNewMaxConns(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		dsn  string
		opts []postgres.Option
		want int32
	}{
		{
			name: "default applies",
			dsn:  validDSN,
			want: postgres.DefaultMaxConns,
		},
		{
			name: "option overrides the default",
			dsn:  validDSN,
			opts: []postgres.Option{postgres.WithMaxConns(25)},
			want: 25,
		},
		{
			name: "DSN pool_max_conns loses to the default",
			dsn:  validDSN + "&pool_max_conns=3",
			want: postgres.DefaultMaxConns,
		},
		{
			name: "DSN pool_max_conns loses to the option",
			dsn:  validDSN + "&pool_max_conns=3",
			opts: []postgres.Option{postgres.WithMaxConns(7)},
			want: 7,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			pool, err := postgres.New(context.Background(), tt.dsn, tt.opts...)
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			defer pool.Close()

			if got := pool.Config().MaxConns; got != tt.want {
				t.Errorf("MaxConns = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestNewStatementTimeout(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		dsnParams string
		opts      []postgres.Option
		want      string
		wantSent  bool
	}{
		{
			name:     "default applies",
			want:     "30000",
			wantSent: true,
		},
		{
			name:     "option overrides the default",
			opts:     []postgres.Option{postgres.WithStatementTimeout(1500 * time.Millisecond)},
			want:     "1500",
			wantSent: true,
		},
		{
			name:      "DSN statement_timeout loses to the default",
			dsnParams: "&statement_timeout=5000",
			want:      "30000",
			wantSent:  true,
		},
		{
			name:      "DSN statement_timeout loses to the option",
			dsnParams: "&statement_timeout=5000",
			opts:      []postgres.Option{postgres.WithStatementTimeout(2 * time.Second)},
			want:      "2000",
			wantSent:  true,
		},
		{
			name: "no-bound option sends none",
			opts: []postgres.Option{postgres.WithoutStatementTimeout()},
		},
		{
			name:      "no-bound option drops the DSN statement_timeout",
			dsnParams: "&statement_timeout=5000",
			opts:      []postgres.Option{postgres.WithoutStatementTimeout()},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			pool, seen := newRecordingPool(t, tt.dsnParams, tt.opts...)

			// Two connections held at once, so the bound is seen on more than
			// the first one the pool opens.
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			first, err := pool.Acquire(ctx)
			if err != nil {
				t.Fatalf("Acquire: %v", err)
			}
			defer first.Release()
			second, err := pool.Acquire(ctx)
			if err != nil {
				t.Fatalf("Acquire: %v", err)
			}
			defer second.Release()

			params := seen.all()
			if len(params) != 2 {
				t.Fatalf("backend saw %d startup messages, want 2", len(params))
			}
			for i, p := range params {
				got, sent := p["statement_timeout"]
				if sent != tt.wantSent || got != tt.want {
					t.Errorf("connection %d: statement_timeout = %q (sent %t), want %q (sent %t)", i, got, sent, tt.want, tt.wantSent)
				}
			}
		})
	}
}

func TestNewRefusesStatementTimeout(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		bound time.Duration
	}{
		{name: "zero", bound: 0},
		{name: "negative", bound: -time.Second},
		{name: "under a millisecond", bound: 500 * time.Microsecond},
		{name: "over what the server accepts", bound: 2147483648 * time.Millisecond},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			pool, err := postgres.New(context.Background(), validDSN, postgres.WithStatementTimeout(tt.bound))
			if err == nil {
				pool.Close()
				t.Fatal("expected an error, got nil")
			}
			if pool != nil {
				t.Error("New returned a pool along with its error")
			}
			if !strings.Contains(err.Error(), "statement_timeout") {
				t.Errorf("error %q does not name statement_timeout", err)
			}
		})
	}
}
