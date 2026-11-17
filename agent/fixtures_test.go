package agent

import (
	"testing"

	"github.com/Autumn-27/artex/db"
)

func testDB(t *testing.T) *db.DB {
	t.Helper()
	dsn, _, err := db.DSN()
	if err != nil {
		t.Skipf("no database config: %v", err)
	}
	d, err := db.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	// Later cleanup callbacks remove fixture rows before the pool is closed.
	t.Cleanup(func() { _ = d.Close() })
	return d
}
