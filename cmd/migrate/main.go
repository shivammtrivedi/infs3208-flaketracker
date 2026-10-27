// cmd/migrate applies (or rolls back) ONE service's database schema.
//
// With database-per-service, every data-owning service has its own Postgres
// instance and its own folder of migrations (db/migrations/<service>). This
// binary is run once per service - locally by hand, and in Kubernetes as
// three separate Jobs - with DB_HOST/DB_NAME/DB_USER/DB_PASSWORD pointing at
// that service's own database. It never touches more than one database per run.
//
// Usage examples:
//
//	go run ./cmd/migrate --service=ingest                  # apply all ingest migrations
//	go run ./cmd/migrate --service=api --direction=down    # roll back every api migration
//	go run ./cmd/migrate --service=analyzer --steps=1      # apply just the next analyzer migration
package main

import (
	"errors"
	"flag"
	"log"
	"path"
	"path/filepath"

	"github.com/golang-migrate/migrate/v4"

	// These two imports are only needed for their side effects, which is
	// what the blank identifier `_` means: we never call anything from them
	// by name. Importing a package runs its init() function, and these
	// packages' init() functions register a "postgres" database driver and a
	// "file" migration-source driver with golang-migrate. Without them,
	// migrate.New below wouldn't understand "postgres://" or "file://" URLs.
	_ "github.com/golang-migrate/migrate/v4/database/postgres"
	_ "github.com/golang-migrate/migrate/v4/source/file"

	"flaketracker/internal/config"
)

// services is the set of services that own a database. A map with bool
// values is Go's idiomatic stand-in for a set (Go has no built-in set type):
// looking up a missing key returns the zero value, false.
var services = map[string]bool{
	"ingest":   true,
	"analyzer": true,
	"api":      true,
}

func main() {
	// The flag package parses command-line arguments like --service=ingest.
	// Each flag.String / flag.Int call returns a *pointer* to the value,
	// which is only filled in once flag.Parse() runs - that's why the code
	// below reads them as *service, *direction, etc. (the * dereferences the
	// pointer to get the actual value).
	service := flag.String("service", "", `which service's database to migrate: "ingest", "analyzer" or "api" (required)`)
	dir := flag.String("dir", "db/migrations", "base directory holding one migrations folder per service")
	direction := flag.String("direction", "up", `migration direction: "up" or "down"`)
	steps := flag.Int("steps", 0, "number of migration steps to apply (0 = all)")
	flag.Parse()

	if !services[*service] {
		log.Fatalf("migrate: --service must be one of ingest, analyzer, api (got %q)", *service)
	}

	// golang-migrate wants a URL with forward slashes ("file://db/migrations/ingest").
	// filepath.ToSlash converts any Windows-style backslashes in --dir, and
	// path.Join (not filepath.Join) always joins with forward slashes.
	source := "file://" + path.Join(filepath.ToSlash(*dir), *service)

	// config.LoadDB reads DB_* from the environment. Whoever runs this
	// command is responsible for pointing those at the right service's
	// database - that's exactly what each Kubernetes Job's env block does.
	dbCfg := config.LoadDB()

	m, err := migrate.New(source, dbCfg.DSN())
	if err != nil {
		log.Fatalf("migrate: init %s: %v", *service, err)
	}

	switch *direction {
	case "up":
		if *steps > 0 {
			err = m.Steps(*steps)
		} else {
			err = m.Up()
		}
	case "down":
		if *steps > 0 {
			err = m.Steps(-*steps)
		} else {
			err = m.Down()
		}
	default:
		log.Fatalf("migrate: unknown direction %q, want \"up\" or \"down\"", *direction)
	}

	// ErrNoChange just means "already up to date" - not a failure, so a Job
	// that runs on every deploy succeeds even when there's nothing new.
	if err != nil && !errors.Is(err, migrate.ErrNoChange) {
		log.Fatalf("migrate: %s %s: %v", *service, *direction, err)
	}
	log.Printf("migrate: %s %s done (database %s on %s)", *service, *direction, dbCfg.Name, dbCfg.Host)
}
