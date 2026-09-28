package engine_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/barad1tos/noxctl/bear/engine"
)

func TestSQLiteTokenRejectsInvalidSchema(t *testing.T) {
	if _, err := exec.LookPath("sqlite3"); err != nil {
		t.Fatalf("sqlite3 not installed: %v", err)
	}
	dbPath := filepath.Join(t.TempDir(), "database.sqlite")
	runSQLiteForTokenTest(t, dbPath, "create table unrelated (id integer);")

	info, err := os.Stat(dbPath)
	if err != nil {
		t.Fatalf("stat database: %v", err)
	}
	_, err = engine.SQLiteNoteChangeToken(dbPath, info)
	if err == nil {
		t.Fatal("expected an error for a database without Bear tables")
	}
}
