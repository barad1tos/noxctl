package bear_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/barad1tos/noxctl/bear"
)

func TestNoteCreatedJSONUnmarshal(t *testing.T) {
	raw := `{"id":"X","title":"Y","content":"Z","created":"2026-05-07T15:06:38Z"}`
	var note bear.Note
	if err := json.Unmarshal([]byte(raw), &note); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	want := time.Date(2026, 5, 7, 15, 6, 38, 0, time.UTC)
	if !note.Created.Equal(want) {
		t.Errorf("Created: got %v, want %v", note.Created, want)
	}
}

func TestNoteCreatedZeroOnMissing(t *testing.T) {
	raw := `{"id":"X","title":"Y","content":"Z"}`
	var note bear.Note
	if err := json.Unmarshal([]byte(raw), &note); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !note.Created.IsZero() {
		t.Errorf("Created should be zero for missing field, got %v", note.Created)
	}
}
