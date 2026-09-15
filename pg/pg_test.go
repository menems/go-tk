package pg_test

import (
	"context"
	"testing"

	"github.com/menems/got-tk/pg"
)

// validDSN points at a port nothing listens on. pgxpool connects lazily, so
// New succeeds and no test here needs a live database.
const validDSN = "postgres://user:pass@127.0.0.1:1/testdb?sslmode=disable"

func TestNewInvalidDSN(t *testing.T) {
	t.Parallel()

	if _, err := pg.New(context.Background(), "://not-a-dsn"); err == nil {
		t.Fatal("expected an error for a malformed DSN, got nil")
	}
}

func TestNewMaxConns(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		dsn  string
		opts []pg.Option
		want int32
	}{
		{
			name: "default applies",
			dsn:  validDSN,
			want: pg.DefaultMaxConns,
		},
		{
			name: "option overrides the default",
			dsn:  validDSN,
			opts: []pg.Option{pg.WithMaxConns(25)},
			want: 25,
		},
		{
			name: "DSN pool_max_conns loses to the default",
			dsn:  validDSN + "&pool_max_conns=3",
			want: pg.DefaultMaxConns,
		},
		{
			name: "DSN pool_max_conns loses to the option",
			dsn:  validDSN + "&pool_max_conns=3",
			opts: []pg.Option{pg.WithMaxConns(7)},
			want: 7,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			pool, err := pg.New(context.Background(), tt.dsn, tt.opts...)
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
