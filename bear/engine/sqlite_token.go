package engine

import (
	"crypto/sha256"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"strings"
)

const sqliteNoteChangeTokenQuery = `
select 'notes';
select Z_PK, Z_OPT, ZVERSION, coalesce(ZMODIFICATIONDATE, 0),
       ZTRASHED, ZARCHIVED, ZPERMANENTLYDELETED
from ZSFNOTE
order by Z_PK;
select 'tags';
select Z_PK, Z_OPT, coalesce(ZMODIFICATIONDATE, 0),
       coalesce(ZTITLE, ''), coalesce(ZTAGCON, '')
from ZSFNOTETAG
order by Z_PK;
select 'links';
select Z_5NOTES, Z_13TAGS
from Z_5TAGS
order by Z_5NOTES, Z_13TAGS;
`

// SQLiteNoteChangeToken returns a stable token for Bear note, tag, and
// note-tag relationship rows without mutating database.sqlite. It opens the
// database through sqlite3's read-only URI mode because Bear/bearcli read APIs
// can still advance the SQLite file mtime through application housekeeping.
func SQLiteNoteChangeToken(dbPath string, _ os.FileInfo) (string, error) {
	dbURL := url.URL{Scheme: "file", Path: dbPath, RawQuery: "mode=ro"}
	output, err := exec.Command("sqlite3", dbURL.String(), sqliteNoteChangeTokenQuery).CombinedOutput()
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
