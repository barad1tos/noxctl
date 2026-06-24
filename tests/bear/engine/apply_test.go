package engine_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/barad1tos/noxctl/bear/bearcli"
	"github.com/barad1tos/noxctl/bear/domain"
	"github.com/barad1tos/noxctl/bear/engine"
	"github.com/barad1tos/noxctl/bear/render"
	"github.com/barad1tos/noxctl/bear/state"
)

func TestApply_NoStateFileLoadsCleanly(t *testing.T) {
	dir := t.TempDir()
	ctx := bearcli.ContextWithBackend(context.Background(), emptyApplyBackend{})
	opts := engine.ApplyOpts{
		Domains:   nil,
		Pins:      nil,
		StatePath: filepath.Join(dir, "state.json"),
		LockPath:  filepath.Join(dir, ".lock"),
		Features:  engine.AllFeaturesOn(),
	}
	result, err := engine.Apply(ctx, opts)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if result.Interrupted {
		t.Errorf("expected Interrupted=false on successful no-domain Apply")
	}
	if result.CompletedAt.IsZero() {
		t.Errorf("expected CompletedAt set on success, got zero")
	}
}

func TestApply_FeaturesGate_DisablesPrePass(t *testing.T) {
	dir := t.TempDir()
	opts := engine.ApplyOpts{
		Domains:   nil,
		StatePath: filepath.Join(dir, "state.json"),
		LockPath:  filepath.Join(dir, ".lock"),
		Features: engine.Features{
			AutoTagDefault:    false,
			CrossDomainMoves:  false,
			TimePromotion:     false,
			ForeignTagEscape:  false,
			DuplicateRegistry: false,
		},
	}
	result, err := engine.Apply(context.Background(), opts)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if _, ok := result.PrePasses["auto_tag"]; ok {
		t.Errorf("auto_tag pre-pass ran despite Features.AutoTagDefault=false")
	}
	if _, ok := result.PrePasses["foreign_tag"]; ok {
		t.Errorf("foreign_tag pre-pass ran despite Features.ForeignTagEscape=false")
	}
}

func TestApply_FeaturesGate_EnablesPrePass(t *testing.T) {
	dir := t.TempDir()
	daily := minimalApplyDomain("stub/daily", "Stub Daily")
	ctx := bearcli.ContextWithBackend(context.Background(), emptyApplyBackend{})
	opts := engine.ApplyOpts{
		Domains:   []*domain.Domain{daily},
		StatePath: filepath.Join(dir, "state.json"),
		LockPath:  filepath.Join(dir, ".lock"),
		Features:  engine.AllFeaturesOn(),
		// Auto-tag fast-pass is now gated on a non-empty
		// DailyDefaultTag in addition to the feature flag — empty
		// tag means "operator omitted [meta].daily_default_tag" and
		// the spec is treated as disabled. Set a synthetic value so
		// AllFeaturesOn actually exercises every pre-pass row here.
		DailyDefaultTag: "stub/daily",
	}
	result, err := engine.Apply(ctx, opts)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if result.AnyFailed() {
		t.Fatalf("AnyFailed = true, want false for hermetic enabled pre-pass test: %#v", result.PrePasses)
	}
	for _, name := range []string{
		"foreign_tag",
		"auto_tag",
		"cross_domain",
		"time_promotion",
		"domain_bootstrap",
		"placeholder_refresh",
		"duplicate_registry",
	} {
		if _, ok := result.PrePasses[name]; !ok {
			t.Errorf("pre-pass %q missing from result.PrePasses", name)
		}
	}
}

func TestApply_DuplicateRegistryRendersURLLinks(t *testing.T) {
	dir := t.TempDir()
	backend := &duplicateLinkApplyBackend{}
	ctx := bearcli.ContextWithBackend(context.Background(), backend)
	d := duplicateLinkApplyDomain()
	opts := engine.ApplyOpts{
		Domains:   []*domain.Domain{d},
		StatePath: filepath.Join(dir, "state.json"),
		LockPath:  filepath.Join(dir, ".lock"),
		Features: engine.Features{
			DuplicateRegistry: true,
		},
		SkipFlock: true,
	}

	result, err := engine.Apply(ctx, opts)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if result.AnyFailed() {
		t.Fatalf("AnyFailed = true, want false: %#v", result)
	}
	body := backend.createdMasterBody()
	for _, noteID := range []string{"note-a", "note-b"} {
		want := "[Same Title](bear://x-callback-url/open-note?id=" + noteID + ")"
		if !strings.Contains(body, want) {
			t.Fatalf("created master body missing %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "[[Same Title]]") {
		t.Fatalf("created master body contains ambiguous wikilink:\n%s", body)
	}
}

type failingApplyBackend struct{}

func (failingApplyBackend) Run(_ context.Context, _ []string, _ string) ([]byte, error) {
	return nil, errors.New("bearcli unavailable")
}

type missingMasterAfterCreateBackend struct{}

func (missingMasterAfterCreateBackend) Run(_ context.Context, args []string, _ string) ([]byte, error) {
	if len(args) == 0 {
		return []byte(`[]`), nil
	}
	switch args[0] {
	case "list":
		return []byte(`[]`), nil
	case "create":
		return nil, errors.New("bearcli create failed")
	case "cat", "show":
		return []byte(`{"id":"missing","title":"missing","content":"","hash":"h","tags":[]}`), nil
	case "overwrite":
		return []byte(`{"ok":true}`), nil
	default:
		return []byte(`[]`), nil
	}
}

type emptyApplyBackend struct{}

func (emptyApplyBackend) Run(_ context.Context, args []string, _ string) ([]byte, error) {
	if len(args) == 0 {
		return []byte(`[]`), nil
	}
	switch args[0] {
	case "list":
		return []byte(`[]`), nil
	case "create":
		return []byte(`{"id":"created","title":"created","content":"","tags":[]}`), nil
	case "cat", "show":
		return []byte(`{"id":"created","title":"created","content":"","hash":"h","tags":[]}`), nil
	case "overwrite":
		return []byte(`{"ok":true}`), nil
	default:
		return []byte(`[]`), nil
	}
}

type duplicateLinkApplyBackend struct {
	mu         sync.Mutex
	masterBody string
}

func (b *duplicateLinkApplyBackend) Run(_ context.Context, args []string, stdin string) ([]byte, error) {
	if len(args) == 0 {
		return []byte(`[]`), nil
	}
	switch args[0] {
	case "list":
		return b.list(args)
	case "create":
		b.mu.Lock()
		b.masterBody = stdin
		b.mu.Unlock()
		return []byte(`{"id":"created-master","title":"Index","content":"","tags":[]}`), nil
	default:
		return []byte(`[]`), nil
	}
}

func (b *duplicateLinkApplyBackend) list(args []string) ([]byte, error) {
	if valueAfter(args, "--location") == "notes" {
		if valueAfter(args, "--fields") != "id,title" {
			return nil, errors.New("heavy corpus read should not be used for duplicate registry")
		}
		return duplicateLinkApplyNotes(), nil
	}
	if valueAfter(args, "--fields") == "id,title" {
		return []byte(`[]`), nil
	}
	return duplicateLinkApplyNotes(), nil
}

type cancelingCrossDomainBackend struct{}

func (cancelingCrossDomainBackend) Run(_ context.Context, args []string, _ string) ([]byte, error) {
	if len(args) > 0 && args[0] == "list" && valueAfter(args, "--fields") == "id,title" {
		return nil, context.Canceled
	}
	if len(args) == 0 {
		return []byte("[]"), nil
	}
	switch args[0] {
	case "cat", "show":
		return []byte(`{"id":"created","title":"created","content":"","hash":"h","tags":[]}`), nil
	case "create":
		return []byte(`{"id":"created","title":"created","content":"","tags":[]}`), nil
	case "overwrite":
		return []byte(`{"ok":true}`), nil
	default:
		return []byte("[]"), nil
	}
}

func (b *duplicateLinkApplyBackend) createdMasterBody() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.masterBody
}

func duplicateLinkApplyNotes() []byte {
	return []byte(`[
		{"id":"note-a","title":"Same Title","content":"# Same Title\n#test/notes | [[Index]] | Items\n---\n","tags":["#test/notes"]},
		{"id":"note-b","title":"Same Title","content":"# Same Title\n#test/notes | [[Index]] | Items\n---\n","tags":["#test/notes"]}
	]`)
}

type crossDomainApplyBackend struct {
	overwriteErr error
}

func (b crossDomainApplyBackend) Run(_ context.Context, args []string, stdin string) ([]byte, error) {
	if len(args) == 0 {
		return []byte(`[]`), nil
	}
	switch args[0] {
	case "list":
		return b.list(args)
	case "cat":
		return b.cat(args)
	case "show":
		return []byte(`{"hash":"deadbeef"}`), nil
	case "create":
		return []byte(`{"id":"created","title":"created","content":"","tags":[]}`), nil
	case "overwrite":
		if b.overwriteErr != nil {
			return nil, b.overwriteErr
		}
		_ = stdin
		return []byte(`{"ok":true}`), nil
	default:
		return []byte(`[]`), nil
	}
}

func (b crossDomainApplyBackend) list(args []string) ([]byte, error) {
	tag := valueAfter(args, "--tag")
	if valueAfter(args, "--fields") == "id,title" {
		switch tag {
		case "inbox/a":
			return []byte(`[{"id":"master-a","title":"Inbox A"}]`), nil
		case "inbox/b":
			return []byte(`[{"id":"master-b","title":"Inbox B"}]`), nil
		}
	}
	if tag == "inbox/a" {
		return []byte(`[` +
			`{"id":"atom-1","title":"Moved","tags":["#inbox/a"],` +
			`"content":"# Moved\n#inbox/a | [[Inbox A]]\n---\nbody\n"}` +
			`]`), nil
	}
	return []byte(`[]`), nil
}

func (b crossDomainApplyBackend) cat(args []string) ([]byte, error) {
	if len(args) < 2 {
		return nil, errors.New("cat missing id")
	}
	switch args[1] {
	case "master-a":
		return []byte(`{"id":"master-a","title":"Inbox A","content":"# Inbox A\n"}`), nil
	case "master-b":
		return []byte(`{"id":"master-b","title":"Inbox B","content":"# Inbox B\n- [[Moved]]\n"}`), nil
	default:
		return []byte(`{"id":"unknown","title":"unknown","content":""}`), nil
	}
}

func valueAfter(args []string, key string) string {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == key {
			return args[i+1]
		}
	}
	return ""
}

func minimalApplyDomain(tag, indexTitle string) *domain.Domain {
	return &domain.Domain{
		Tag:          tag,
		CanonicalTag: "#" + tag,
		IndexTitle:   indexTitle,
		ParseMeta: func(_ *domain.Domain, _ string) domain.AtomicMeta {
			return domain.AtomicMeta{}
		},
		RenderMaster: func(_ *domain.Domain, _ map[string][]domain.Note) string {
			return "# " + indexTitle + "\n"
		},
	}
}

func duplicateLinkApplyDomain() *domain.Domain {
	return &domain.Domain{
		Tag:             "test/notes",
		CanonicalTag:    "#test/notes",
		IndexTitle:      "Index",
		UnknownBucket:   "Items",
		SkipAtomicsPass: true,
		ParseMeta: func(_ *domain.Domain, _ string) domain.AtomicMeta {
			return domain.AtomicMeta{Bucket: "Items"}
		},
		RenderMaster: func(d *domain.Domain, groups map[string][]domain.Note) string {
			var body strings.Builder
			body.WriteString("# Index\n#test/notes\n---\n")
			domain.RenderNoteList(&body, d, groups["Items"])
			return body.String()
		},
	}
}

func TestApply_DomainListFailureSurfacesInResult(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, "state.json")
	priorLastApply := time.Date(2026, 5, 26, 12, 0, 0, 0, time.UTC)
	if err := (&state.State{Version: state.SchemaVersion, LastApply: priorLastApply}).Save(statePath); err != nil {
		t.Fatalf("seed state: %v", err)
	}
	d := minimalApplyDomain("test/failing", "Test Failing")
	ctx := bearcli.ContextWithBackend(context.Background(), failingApplyBackend{})
	opts := engine.ApplyOpts{
		Domains:   []*domain.Domain{d},
		StatePath: statePath,
		LockPath:  filepath.Join(dir, ".lock"),
		Features:  engine.Features{},
	}
	result, err := engine.Apply(ctx, opts)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !result.AnyFailed() {
		t.Fatal("AnyFailed = false, want true for per-domain list failure")
	}
	if got := result.Domains[d.Tag].Failed; got != 1 {
		t.Errorf("Failed = %d, want 1", got)
	}
	after, err := state.Load(statePath)
	if err != nil {
		t.Fatalf("Load state: %v", err)
	}
	if !after.LastApply.Equal(priorLastApply) {
		t.Errorf("LastApply = %s, want prior %s", after.LastApply, priorLastApply)
	}
	if after.InProgress.Verb != "apply" {
		t.Errorf("InProgress.Verb = %q, want apply after failed run", after.InProgress.Verb)
	}
	if !result.CompletedAt.IsZero() {
		t.Errorf("CompletedAt = %s, want zero after failed run", result.CompletedAt)
	}
}

func TestApply_MissingMasterSnapshotPreservesPriorContentHash(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, "state.json")
	d := minimalApplyDomain("test/missing-master", "Test Missing Master")
	priorHash := "previous-content-hash"
	if err := (&state.State{
		Version: state.SchemaVersion,
		Domains: map[string]state.DomainState{
			d.Tag: {ContentHash: priorHash},
		},
	}).Save(statePath); err != nil {
		t.Fatalf("seed state: %v", err)
	}

	ctx := bearcli.ContextWithBackend(context.Background(), missingMasterAfterCreateBackend{})
	result, err := engine.Apply(ctx, engine.ApplyOpts{
		Domains:   []*domain.Domain{d},
		StatePath: statePath,
		LockPath:  filepath.Join(dir, ".lock"),
		Features:  engine.Features{},
		SkipFlock: true,
	})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !result.AnyFailed() {
		t.Fatal("AnyFailed = false, want failed result after master create failure")
	}
	after, err := state.Load(statePath)
	if err != nil {
		t.Fatalf("Load state: %v", err)
	}
	if got := after.Domains[d.Tag].ContentHash; got != priorHash {
		t.Fatalf("ContentHash = %q, want prior hash %q after missing-master snapshot", got, priorHash)
	}
}

func TestApply_DaemonFailureUsesDaemonProgressMarker(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, "state.json")
	priorLastApply := time.Date(2026, 5, 26, 12, 0, 0, 0, time.UTC)
	if err := (&state.State{Version: state.SchemaVersion, LastApply: priorLastApply}).Save(statePath); err != nil {
		t.Fatalf("seed state: %v", err)
	}
	d := minimalApplyDomain("test/daemon-failing", "Test Daemon Failing")
	ctx := bearcli.ContextWithBackend(context.Background(), failingApplyBackend{})
	opts := engine.ApplyOpts{
		Domains:   []*domain.Domain{d},
		StatePath: statePath,
		LockPath:  filepath.Join(dir, ".lock"),
		Features:  engine.Features{},
		SkipFlock: true,
	}
	result, err := engine.Apply(ctx, opts)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !result.AnyFailed() {
		t.Fatal("AnyFailed = false, want true for daemon list failure")
	}
	after, err := state.Load(statePath)
	if err != nil {
		t.Fatalf("Load state: %v", err)
	}
	if !after.LastApply.Equal(priorLastApply) {
		t.Errorf("LastApply = %s, want prior %s", after.LastApply, priorLastApply)
	}
	if after.InProgress.Verb != "daemon" {
		t.Errorf("InProgress.Verb = %q, want daemon after failed daemon cycle", after.InProgress.Verb)
	}
}

func TestApply_MasterCreateSurfacesInDomainCounts(t *testing.T) {
	dir := t.TempDir()
	d := minimalApplyDomain("test/master-create", "Test Master Create")
	ctx := bearcli.ContextWithBackend(context.Background(), emptyApplyBackend{})
	opts := engine.ApplyOpts{
		Domains:   []*domain.Domain{d},
		StatePath: filepath.Join(dir, "state.json"),
		LockPath:  filepath.Join(dir, ".lock"),
		Features:  engine.Features{},
	}
	result, err := engine.Apply(ctx, opts)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	counts := result.Domains[d.Tag]
	if counts.Created != 1 {
		t.Errorf("Created = %d, want 1 for master create", counts.Created)
	}
	if counts.Unchanged != 0 {
		t.Errorf("Unchanged = %d, want 0 when master was created", counts.Unchanged)
	}
}

func TestApply_CrossDomainMoveSurfacesChangedPrePassCount(t *testing.T) {
	dir := t.TempDir()
	domains := []*domain.Domain{
		render.NewFlatListDomain("inbox/a", "Inbox A"),
		render.NewFlatListDomain("inbox/b", "Inbox B"),
	}
	ctx := bearcli.ContextWithBackend(context.Background(), crossDomainApplyBackend{})
	result, err := engine.Apply(ctx, engine.ApplyOpts{
		Domains:   domains,
		StatePath: filepath.Join(dir, "state.json"),
		LockPath:  filepath.Join(dir, ".lock"),
		Features:  engine.Features{CrossDomainMoves: true},
	})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	counts := result.PrePasses["cross_domain"]
	if counts.Changed != 1 || counts.Failed != 0 {
		t.Fatalf("cross_domain counts = %+v, want changed=1 failed=0", counts)
	}
}

func TestApply_CrossDomainMoveSurfacesFailedPrePassCount(t *testing.T) {
	dir := t.TempDir()
	domains := []*domain.Domain{
		render.NewFlatListDomain("inbox/a", "Inbox A"),
		render.NewFlatListDomain("inbox/b", "Inbox B"),
	}
	ctx := bearcli.ContextWithBackend(context.Background(), crossDomainApplyBackend{
		overwriteErr: errors.New("overwrite failed"),
	})
	result, err := engine.Apply(ctx, engine.ApplyOpts{
		Domains:   domains,
		StatePath: filepath.Join(dir, "state.json"),
		LockPath:  filepath.Join(dir, ".lock"),
		Features:  engine.Features{CrossDomainMoves: true},
	})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	counts := result.PrePasses["cross_domain"]
	if counts.Changed != 0 || counts.Failed != 1 {
		t.Fatalf("cross_domain counts = %+v, want changed=0 failed=1", counts)
	}
}

func TestApply_CrossDomainMoveCancelMarksInterruptedWithoutFailure(t *testing.T) {
	dir := t.TempDir()
	domains := []*domain.Domain{
		render.NewFlatListDomain("inbox/a", "Inbox A"),
		render.NewFlatListDomain("inbox/b", "Inbox B"),
	}
	ctx := bearcli.ContextWithBackend(context.Background(), cancelingCrossDomainBackend{})

	result, err := engine.Apply(ctx, engine.ApplyOpts{
		Domains:   domains,
		StatePath: filepath.Join(dir, "state.json"),
		LockPath:  filepath.Join(dir, ".lock"),
		Features:  engine.Features{CrossDomainMoves: true},
		SkipFlock: true,
	})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	counts := result.PrePasses["cross_domain"]
	if counts.Failed != 0 {
		t.Fatalf("cross_domain counts = %+v, want canceled pre-pass without synthetic failure", counts)
	}
	if !result.Interrupted {
		t.Fatal("Interrupted = false, want true when a pre-pass sees context cancellation")
	}
}

// TestApply_AutoTagGatedOnDailyDefaultTag pins the "empty tag = silent
// disable" contract for the daily-default fast-pass while preserving
// placeholder-refresh independence:
//
//   - auto_tag is skipped when DailyDefaultTag is empty — earlier
//     wiring called ApplyDailyDefaultTag with a nil domain (map
//     lookup on empty key) which returned an error and stamped a
//     spurious Failed=1 in the result.
//   - placeholder_refresh must still run because ApplyPlaceholder
//     Refresh has no dependency on the daily tag — it iterates every
//     domain with a non-empty QuickPlaceholderH1. Folding the daily
//     gate into both passes would silently disable placeholder
//     refresh for catalogs that set `quick_placeholder_h1` on a
//     domain without declaring `[meta].daily_default_tag`.
func TestApply_AutoTagGatedOnDailyDefaultTag(t *testing.T) {
	dir := t.TempDir()
	ctx := bearcli.ContextWithBackend(context.Background(), emptyApplyBackend{})
	opts := engine.ApplyOpts{
		Domains:   nil,
		StatePath: filepath.Join(dir, "state.json"),
		LockPath:  filepath.Join(dir, ".lock"),
		Features:  engine.AllFeaturesOn(),
		// DailyDefaultTag deliberately empty.
	}
	result, err := engine.Apply(ctx, opts)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if result.AnyFailed() {
		t.Fatalf("AnyFailed = true, want false for hermetic auto-tag gate test: %#v", result.PrePasses)
	}
	if _, ok := result.PrePasses["auto_tag"]; ok {
		t.Error("pre-pass \"auto_tag\" ran despite empty DailyDefaultTag")
	}
	if _, ok := result.PrePasses["placeholder_refresh"]; !ok {
		t.Error("pre-pass \"placeholder_refresh\" was skipped; it must stay on " +
			"Features.AutoTagDefault alone (independent of DailyDefaultTag)")
	}
}

func TestApply_StateOnSuccessClearsInProgress(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, "state.json")
	opts := engine.ApplyOpts{
		Domains:   nil,
		StatePath: statePath,
		LockPath:  filepath.Join(dir, ".lock"),
		Features:  engine.Features{}, // all false to skip pre-passes that need bearcli
	}
	if _, err := engine.Apply(context.Background(), opts); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	raw, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	var st state.State
	if err = json.Unmarshal(raw, &st); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if st.InProgress.Verb != "" {
		t.Errorf("expected InProgress cleared after success, got Verb=%q", st.InProgress.Verb)
	}
	if st.LastApply.IsZero() {
		t.Errorf("expected LastApply set after success, got zero")
	}
}

func TestApply_ContentHashStable_StripsNewNoteLink(t *testing.T) {
	// ComputeContentHash is exported in package engine (see apply.go
	// docstring for the project-policy deviation that motivated
	// exporting rather than using a test-seam shim). The strip-of-
	// new-note-link happens inside regen.FetchMasterContent
	// (snapshot.go), NOT inside ComputeContentHash — so this test
	// verifies that ComputeContentHash is deterministic on already-
	// stripped inputs (the strip-then-hash discipline at the pipeline
	// boundary).
	masterStripped := "## Поезії\n[Author One]\n"
	h1 := engine.ComputeContentHash(masterStripped, nil)
	h2 := engine.ComputeContentHash(masterStripped, nil)
	if h1 != h2 {
		t.Errorf("content hash non-deterministic: %q vs %q", h1, h2)
	}
	// With one hub, hash differs from no-hub:
	h3 := engine.ComputeContentHash(masterStripped, map[string]string{"Hub A": "body"})
	if h1 == h3 {
		t.Errorf("hash should differ when hubs added; both %q", h1)
	}
}
