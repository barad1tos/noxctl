package engine

import (
	"crypto/sha256"
	_ "embed"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

const (
	sqliteFieldSeparator = "\x1f"
	sqliteRowSeparator   = "\x1e"
)

//go:embed sqlite_note_change_token.sql
var sqliteNoteChangeTokenQuery string

// SQLiteNoteChangeToken returns a stable token for Bear note, tag, and
// note-tag relationship rows without mutating database.sqlite. It opens the
// database through sqlite3's read-only mode because Bear/bearcli read APIs
// can still advance the SQLite file mtime through application housekeeping.
func SQLiteNoteChangeToken(dbPath string, _ os.FileInfo) (string, error) {
	output, err := exec.Command(
		"sqlite3",
		"-batch",
		"-bail",
		"-readonly",
		"-separator", sqliteFieldSeparator,
		"-newline", sqliteRowSeparator,
		"-cmd", "PRAGMA busy_timeout = 5000;",
		"-cmd", "PRAGMA query_only = ON;",
		dbPath,
		sqliteNoteChangeTokenQuery,
	).CombinedOutput()
	if err != nil {
		stderr := strings.TrimSpace(string(output))
		if stderr == "" {
			return "", fmt.Errorf("sqlite note change token: %w", err)
		}
		return "", fmt.Errorf("sqlite note change token: %w: %s", err, stderr)
	}
	sum := sha256.Sum256(output)
	return fmt.Sprintf("sha256:%x", sum), nil
}
