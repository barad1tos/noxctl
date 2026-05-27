package apply_test

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/barad1tos/noxctl/bear/cli"
	"github.com/barad1tos/noxctl/bear/config"
	"github.com/barad1tos/noxctl/bear/domain"
	"github.com/barad1tos/noxctl/bear/render"
)

type failingBackend struct{}

func (failingBackend) Run(_ context.Context, _ []string, _ string) ([]byte, error) {
	return nil, errors.New("bearcli unavailable")
}

func falsePtr() *bool {
	value := false
	return &value
}

func truePtr() *bool {
	value := true
	return &value
}

func disabledFeatureCatalog() *config.Catalog {
	return &config.Catalog{
		Features: config.Features{
			AutoTagDefault:    falsePtr(),
			CrossDomainMoves:  falsePtr(),
			TimePromotion:     falsePtr(),
			ForeignTagEscape:  falsePtr(),
			DuplicateRegistry: falsePtr(),
			DomainBootstrap:   falsePtr(),
		},
	}
}

func failingDomain() *domain.Domain {
	return &domain.Domain{
		Tag:          "test/failing",
		CanonicalTag: "#test/failing",
		IndexTitle:   "Test Failing",
		ParseMeta: func(_ *domain.Domain, _ string) domain.AtomicMeta {
			return domain.AtomicMeta{}
		},
		RenderMaster: func(_ *domain.Domain, _ map[string][]domain.Note) string {
			return "# Test Failing\n"
		},
	}
}

func TestRunApply_DomainFailureReturnsFailureSentinel(t *testing.T) {
	dir := t.TempDir()
	ctx := domain.ContextWithBackend(context.Background(), failingBackend{})
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	err := cli.RunApply(ctx, cli.ApplyOptions{
		Domains:   []*domain.Domain{failingDomain()},
		Catalog:   disabledFeatureCatalog(),
		PinTarget: filepath.Join(dir, "pins.json"),
		StatePath: filepath.Join(dir, "state.json"),
		LockPath:  filepath.Join(dir, ".lock"),
		Quiet:     true,
		Stdout:    &stdout,
		Stderr:    &stderr,
	})
	if !errors.Is(err, cli.ErrApplyFailures) {
		t.Fatalf("RunApply err = %v, want ErrApplyFailures", err)
	}
	if !strings.Contains(stdout.String(), "failed=1") {
		t.Fatalf("stdout = %q, want failed=1 recap row", stdout.String())
	}
}

type promotionOverwriteFailBackend struct{}

func (promotionOverwriteFailBackend) Run(_ context.Context, args []string, _ string) ([]byte, error) {
	if len(args) == 0 {
		return []byte(`[]`), nil
	}
	switch args[0] {
	case "list":
		for i := 0; i+1 < len(args); i++ {
			if args[i] == "--tag" && args[i+1] == "test/daily" {
				return []byte(`[{"id":"atom-1","title":"Aged","tags":["#test/daily"],"content":"# Aged\n#test/daily | [[Daily]]\n---\nbody\n","created":"2020-01-01T00:00:00Z"}]`), nil
			}
		}
		return []byte(`[]`), nil
	case "create", "cat", "show":
		return []byte(`{"id":"created","title":"created","content":"","hash":"h","tags":[]}`), nil
	case "overwrite":
		return nil, errors.New("overwrite failed")
	default:
		return []byte(`[]`), nil
	}
}

func timePromotionCatalog() *config.Catalog {
	catalog := disabledFeatureCatalog()
	catalog.Features.TimePromotion = truePtr()
	catalog.Promotions = []config.Promotion{
		{From: "test/daily", To: "test/weekly", Boundary: "day"},
	}
	return catalog
}

func TestRunApply_PrePassWriteFailureReturnsFailureSentinel(t *testing.T) {
	dir := t.TempDir()
	daily := render.NewFlatListDomain("test/daily", "Daily")
	weekly := render.NewFlatListDomain("test/weekly", "Weekly")
	ctx := domain.ContextWithBackend(context.Background(), promotionOverwriteFailBackend{})
	var stdout bytes.Buffer

	err := cli.RunApply(ctx, cli.ApplyOptions{
		Domains:   []*domain.Domain{daily, weekly},
		Catalog:   timePromotionCatalog(),
		PinTarget: filepath.Join(dir, "pins.json"),
		StatePath: filepath.Join(dir, "state.json"),
		LockPath:  filepath.Join(dir, ".lock"),
		Quiet:     true,
		Stdout:    &stdout,
	})
	if !errors.Is(err, cli.ErrApplyFailures) {
		t.Fatalf("RunApply err = %v, want ErrApplyFailures", err)
	}
	if !strings.Contains(stdout.String(), "time_promotion") || !strings.Contains(stdout.String(), "failed=1") {
		t.Fatalf("stdout = %q, want time_promotion failed=1 recap row", stdout.String())
	}
}
