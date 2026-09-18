// Package migrate applies SQL migrations to a PostgreSQL database, out of
// band, from a command of the consuming module.
//
// The consumer owns two things this package cannot: the migration files, which
// it embeds and hands over as an fs.FS, and the DSN, which it reads at its own
// boot. Everything else, the argument grammar, the driver wiring and the
// version reporting, is the same in every service and lives here.
//
// The upstream library is imported under an alias: this package is named after
// what it does for a consumer, and `migrate` inside it is ours, not theirs.
package migrate

import (
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"strconv"

	golangmigrate "github.com/golang-migrate/migrate/v4"
	pgxmigrate "github.com/golang-migrate/migrate/v4/database/pgx/v5"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	_ "github.com/jackc/pgx/v5/stdlib"
)

// Usage is the argument grammar Run accepts, ready to print.
const Usage = "usage: migrate up | down <N> | version"

// ErrUsage reports an argument list Run could not parse. It is returned before
// any connection is opened, so a typo costs nothing. A command exits 2 on it.
var ErrUsage = errors.New(Usage)

// command is one parsed subcommand; steps is only meaningful for "down".
type command struct {
	name  string
	steps int
}

// parse turns the argument list into a command before any connection is
// opened, so a typo fails without touching the database.
func parse(args []string) (command, error) {
	if len(args) == 0 {
		return command{}, ErrUsage
	}

	switch args[0] {
	case "up", "version":
		if len(args) != 1 {
			return command{}, ErrUsage
		}
		return command{name: args[0]}, nil
	case "down":
		if len(args) != 2 {
			return command{}, ErrUsage
		}
		steps, err := strconv.Atoi(args[1])
		if err != nil || steps < 1 {
			return command{}, ErrUsage
		}
		return command{name: "down", steps: steps}, nil
	default:
		return command{}, ErrUsage
	}
}

// Run applies args to the database at dsn, using the migrations in fsys.
//
// It reads no environment and opens nothing until the arguments parse: the
// caller reads DATABASE_URL in its own main, where a missing value stops the
// process by name.
//
// There is no context parameter: golang-migrate's API predates one, and a
// migration in flight is not cancellable. Pretending otherwise with an
// ignored ctx would be worse than its absence.
func Run(logger *slog.Logger, dsn string, fsys fs.FS, args []string) error {
	cmd, err := parse(args)
	if err != nil {
		return err
	}
	if dsn == "" {
		return errors.New("migrate: empty dsn")
	}

	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return fmt.Errorf("migrate: open database: %w", err)
	}
	defer db.Close()

	driver, err := pgxmigrate.WithInstance(db, &pgxmigrate.Config{})
	if err != nil {
		return fmt.Errorf("migrate: create driver: %w", err)
	}

	source, err := iofs.New(fsys, ".")
	if err != nil {
		return fmt.Errorf("migrate: load source: %w", err)
	}

	m, err := golangmigrate.NewWithInstance("iofs", source, "pgx5", driver)
	if err != nil {
		return fmt.Errorf("migrate: create migrator: %w", err)
	}

	switch cmd.name {
	case "up":
		err = m.Up()
	case "down":
		err = m.Steps(-cmd.steps)
	case "version":
		return logVersion(logger, m)
	}
	if errors.Is(err, golangmigrate.ErrNoChange) {
		logger.Info("no change", "command", cmd.name)
		return nil
	}
	if err != nil {
		return fmt.Errorf("migrate %s: %w", cmd.name, err)
	}

	return logVersion(logger, m)
}

func logVersion(logger *slog.Logger, m *golangmigrate.Migrate) error {
	version, dirty, err := m.Version()
	if errors.Is(err, golangmigrate.ErrNilVersion) {
		logger.Info("schema version", "version", "none")
		return nil
	}
	if err != nil {
		return fmt.Errorf("migrate: read schema version: %w", err)
	}

	logger.Info("schema version", "version", version, "dirty", dirty)
	return nil
}
