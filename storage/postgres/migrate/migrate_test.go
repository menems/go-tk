package migrate_test

import (
	"errors"
	"io/fs"
	"log/slog"
	"testing"
	"testing/fstest"

	"github.com/menems/go-tk/storage/postgres/migrate"
)

// discard keeps the test output clean; nothing here asserts on the log.
func discard() *slog.Logger { return slog.New(slog.DiscardHandler) }

// empty stands in for the consumer's embedded migrations. No test here reaches
// far enough to read it: Run parses its arguments, and validates the DSN,
// before it opens anything.
var empty fs.FS = fstest.MapFS{}

func TestRunRejectsBadArguments(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		args []string
	}{
		{name: "no argument", args: nil},
		{name: "unknown subcommand", args: []string{"sideways"}},
		{name: "up with an extra argument", args: []string{"up", "2"}},
		{name: "version with an extra argument", args: []string{"version", "2"}},
		{name: "down without a count", args: []string{"down"}},
		{name: "down with a non-number", args: []string{"down", "two"}},
		{name: "down with zero", args: []string{"down", "0"}},
		{name: "down with a negative count", args: []string{"down", "-1"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// A live DSN would prove nothing: the grammar is checked first.
			err := migrate.Run(discard(), "postgres://unused", empty, tt.args)
			if !errors.Is(err, migrate.ErrUsage) {
				t.Fatalf("Run(%q) = %v, want ErrUsage", tt.args, err)
			}
		})
	}
}

func TestRunAcceptsTheGrammarBeforeDialing(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		args []string
	}{
		{name: "up", args: []string{"up"}},
		{name: "version", args: []string{"version"}},
		{name: "down with a count", args: []string{"down", "3"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// An empty DSN is refused after the grammar passed, which is how
			// this test tells "the arguments were accepted" from "they were
			// not" without a database.
			err := migrate.Run(discard(), "", empty, tt.args)
			if errors.Is(err, migrate.ErrUsage) {
				t.Fatalf("Run(%q) = ErrUsage, want the arguments accepted", tt.args)
			}
			if err == nil {
				t.Fatalf("Run(%q) with an empty DSN = nil, want an error", tt.args)
			}
		})
	}
}
