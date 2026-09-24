// Package postgres opens a PostgreSQL connection pool from a DSN, with every
// statement bounded by a server-side timeout, and reads the errors pgx returns
// through Classify and its sentinels.
//
// New does not dial: pgxpool connects lazily on the first query. Whether the
// database is reachable is a readiness question, answered by health, to
// which *pgxpool.Pool's Ping method can be handed directly.
package postgres

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// DefaultMaxConns is the pool size New applies when WithMaxConns is not given.
const DefaultMaxConns = 10

// DefaultStatementTimeout is the bound New sets on every statement when neither
// WithStatementTimeout nor WithoutStatementTimeout is given.
const DefaultStatementTimeout = 30 * time.Second

// statementTimeout is the server setting that carries the bound, sent as a
// startup parameter on every connection the pool opens.
const statementTimeout = "statement_timeout"

// Option adjusts the pool configuration parsed from the DSN.
type Option func(*pgxpool.Config)

// WithMaxConns sets the maximum number of connections the pool may open.
func WithMaxConns(n int32) Option {
	return func(c *pgxpool.Config) { c.MaxConns = n }
}

// WithStatementTimeout sets the server's statement_timeout on every connection
// the pool opens, in whole milliseconds. New refuses a bound under one
// millisecond or over what the server accepts (2147483647 ms).
func WithStatementTimeout(d time.Duration) Option {
	return func(c *pgxpool.Config) {
		c.ConnConfig.RuntimeParams[statementTimeout] = strconv.FormatInt(d.Milliseconds(), 10)
	}
}

// WithoutStatementTimeout sends no statement_timeout at all, a DSN value
// included. It is for a pool behind a pooler that refuses the parameter at
// startup, as PgBouncer does by default: its statements are then bounded only
// by what the role or the pooler sets.
func WithoutStatementTimeout() Option {
	return func(c *pgxpool.Config) { delete(c.ConnConfig.RuntimeParams, statementTimeout) }
}

// New parses dsn and opens a connection pool. The caller owns the pool and
// closes it.
//
// New owns the pool size: a pool_max_conns in the DSN is overwritten by
// DefaultMaxConns or by WithMaxConns. Two places to size a pool is one too
// many, and the option is the one a service can compute at boot.
//
// New owns the statement bound the same way: a statement_timeout in the DSN is
// overwritten by DefaultStatementTimeout or by WithStatementTimeout, and
// dropped by WithoutStatementTimeout. An invalid bound is an error, and no pool
// is returned.
func New(ctx context.Context, dsn string, opts ...Option) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("pg: parse dsn: %w", err)
	}
	cfg.MaxConns = DefaultMaxConns
	WithStatementTimeout(DefaultStatementTimeout)(cfg)

	for _, opt := range opts {
		opt(cfg)
	}
	if err := checkStatementTimeout(cfg); err != nil {
		return nil, err
	}

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("pg: open pool: %w", err)
	}
	return pool, nil
}

// checkStatementTimeout reads the bound the options left rather than the
// duration WithStatementTimeout was given, so Option keeps its signature and a
// value the server would refuse fails here, at boot, not at every connection.
func checkStatementTimeout(cfg *pgxpool.Config) error {
	v, set := cfg.ConnConfig.RuntimeParams[statementTimeout]
	if !set {
		return nil
	}
	ms, err := strconv.ParseInt(v, 10, 32)
	if err != nil || ms <= 0 {
		return fmt.Errorf("pg: %s %q: want whole milliseconds from 1 to %d", statementTimeout, v, math.MaxInt32)
	}
	return nil
}
