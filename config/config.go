// Package config reads configuration from an environment at boot, collecting
// every problem before it reports.
//
// The service declares its own Config struct and fills it through an Env, so
// the fields, their names and their defaults stay in the service where they
// are read. What this package owns is the reading: one place that records a
// missing or malformed value instead of returning on the first, and a getenv
// that is a parameter rather than a call to the process environment.
//
//	func Load(getenv func(string) string) (Config, error) {
//	    env := config.New(getenv)
//	    cfg := Config{
//	        DatabaseURL: env.Required("DATABASE_URL"),
//	        Addr:        env.String("HTTP_ADDR", ":8080"),
//	        MaxConns:    env.Int("DB_MAX_CONNS", 10),
//	        Timeout:     env.Duration("REQUEST_TIMEOUT", 5*time.Second),
//	    }
//	    return cfg, env.Err()
//	}
//
// An Env is read once, in one goroutine, at boot. It is not safe for
// concurrent use.
package config

import (
	"errors"
	"fmt"
	"strconv"
	"time"
)

// Env reads configuration values and remembers what went wrong.
type Env struct {
	getenv func(string) string
	errs   []error
}

// New returns an Env reading through getenv. Pass os.Getenv in main, and a
// map's lookup in a test, so a config test needs no process environment and
// runs in parallel.
func New(getenv func(string) string) *Env {
	return &Env{getenv: getenv}
}

// Err returns every problem the Env collected, joined, or nil when there was
// none. Call it once, after the whole struct is read: a boot that stops at the
// first missing value makes its operator fix them one deploy at a time.
func (e *Env) Err() error { return errors.Join(e.errs...) }

// Required returns the value of key, and records a problem when it is unset or
// empty.
//
// Only a string can be required, because the values with no sensible default
// are DSNs, endpoints and secrets. A port or a timeout that reaches production
// unset wants a default, not a boot failure.
func (e *Env) Required(key string) string {
	v := e.getenv(key)
	if v == "" {
		e.fail(key, "is required")
	}
	return v
}

// String returns the value of key, or fallback when it is unset or empty.
func (e *Env) String(key, fallback string) string {
	if v := e.getenv(key); v != "" {
		return v
	}
	return fallback
}

// Int returns the value of key parsed as an integer, or fallback when it is
// unset or empty. A value that is set but unparseable is a problem, not a
// reason to fall back quietly.
func (e *Env) Int(key string, fallback int) int {
	raw := e.getenv(key)
	if raw == "" {
		return fallback
	}

	v, err := strconv.Atoi(raw)
	if err != nil {
		e.fail(key, "is not an integer")
		return fallback
	}
	return v
}

// Duration returns the value of key parsed as a time.Duration, or fallback
// when it is unset or empty.
func (e *Env) Duration(key string, fallback time.Duration) time.Duration {
	raw := e.getenv(key)
	if raw == "" {
		return fallback
	}

	v, err := time.ParseDuration(raw)
	if err != nil {
		e.fail(key, "is not a duration, which looks like 5s, 250ms or 1h30m")
		return fallback
	}
	return v
}

// fail records a problem naming the key. The value stays out of the message:
// a malformed DATABASE_URL carries a password, and a boot error is logged.
func (e *Env) fail(key, problem string) {
	e.errs = append(e.errs, fmt.Errorf("config: %s %s", key, problem))
}
