// Package testpg coordinates integration test packages sharing one PostgreSQL DB.
package testpg

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"time"

	"github.com/Autumn-27/artex/config"
	_ "github.com/jackc/pgx/v5/stdlib"
)

const suiteLockKey int64 = 7337741002

// Run holds the suite lock on one connection, before any package applies DDL.
// With no DB configured, individual integration tests retain their usual skips.
func Run(run func() int) int {
	dsn, _, err := config.PostgresDSN()
	if err != nil {
		return run()
	}
	pg, err := sql.Open("pgx", dsn)
	if err != nil {
		fmt.Fprintln(os.Stderr, "[testpg] open:", err)
		return 1
	}
	defer pg.Close()
	pg.SetMaxOpenConns(1)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	conn, err := pg.Conn(ctx)
	cancel()
	if err != nil {
		fmt.Fprintln(os.Stderr, "[testpg] connect:", err)
		return 1
	}
	defer conn.Close()
	if _, err := conn.ExecContext(context.Background(), `SELECT pg_advisory_lock($1)`, suiteLockKey); err != nil {
		fmt.Fprintln(os.Stderr, "[testpg] lock:", err)
		return 1
	}
	defer conn.ExecContext(context.Background(), `SELECT pg_advisory_unlock($1)`, suiteLockKey) //nolint:errcheck
	return run()
}
