package bear_test

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/barad1tos/noxctl/bear/domain"
	"github.com/barad1tos/noxctl/bear/render"
)

type hubUpsertBackend struct {
	fullList    []domain.Note
	titleList   []domain.Note
	catByID     map[string]string
	creates     []string
	overwrites  []fakeOverwriteCall
	overwriteID string
}

func (b *hubUpsertBackend) Run(_ context.Context, args []string, stdin string) ([]byte, error) {
	if len(args) == 0 {
		return []byte("[]"), nil
	}
	switch args[0] {
	case "list":
		if strings.Contains(strings.Join(args, ","), "id,title,content,tags,created") {
			return json.Marshal(b.fullList)
		}
		return json.Marshal(b.titleList)
	case "create":
		if len(args) < 2 {
			return nil, errors.New("create missing title")
		}
		b.creates = append(b.creates, args[1])
		return json.Marshal(domain.Note{ID: "created-" + args[1], Title: args[1]})
	case "cat":
		if len(args) < 2 {
			return nil, errors.New("cat missing id")
		}
		return json.Marshal(domain.Note{ID: args[1], Content: b.catByID[args[1]]})
	case "show":
		return []byte(`{"hash":"deadbeef"}`), nil
	case "overwrite":
		if len(args) < 2 {
			return nil, errors.New("overwrite missing id")
		}
		b.overwriteID = args[1]
		b.overwrites = append(b.overwrites, fakeOverwriteCall{NoteID: args[1], Content: stdin})
		return []byte(`{"ok":true}`), nil
	default:
		return []byte("{}"), nil
	}
}

func hubUpsertDomain() *domain.Domain {
	d := render.NewHubRoutedDomain(
		"library/poetry",
		"Poetry Index",
		"Unknown",
		"Poems",
		render.DefaultRenderMaster3Tier,
	)
	d.SkipAtomicsPass = true
	return d
}

func hubUpsertAtom() domain.Note {
	return domain.Note{
		ID:      "atom-1",
		Title:   "Poem One",
		Content: "# Poem One\n#library/poetry | [[Biko]]\n---\nbody\n",
		Tags:    []string{"#library/poetry"},
	}
}

func TestRunRegen_HubCreateSurfacesInResult(t *testing.T) {
	d := hubUpsertDomain()
	backend := &hubUpsertBackend{fullList: []domain.Note{hubUpsertAtom()}}
	ctx := domain.ContextWithBackend(context.Background(), backend)

	result := d.RunRegen(ctx)
	if result.HubsCreated != 1 || result.HubsChanged != 0 || result.HubsFailed != 0 {
		t.Fatalf("hub result = created:%d changed:%d failed:%d, want 1/0/0",
			result.HubsCreated, result.HubsChanged, result.HubsFailed)
	}
	if !slices.Contains(backend.creates, "Biko") {
		t.Fatalf("created titles = %v, want hub title Biko", backend.creates)
	}
}

func TestRunRegen_HubUpdateSurfacesInResult(t *testing.T) {
	d := hubUpsertDomain()
	masterContent := render.DefaultRenderMaster3Tier(d, map[string][]domain.Note{
		"Biko": {hubUpsertAtom()},
	})
	backend := &hubUpsertBackend{
		fullList:  []domain.Note{hubUpsertAtom()},
		titleList: []domain.Note{{ID: "hub-biko", Title: "Biko"}, {ID: "master", Title: "Poetry Index"}},
		catByID: map[string]string{
			"hub-biko": "# Biko\nstale\n",
			"master":   masterContent,
		},
	}
	ctx := domain.ContextWithBackend(context.Background(), backend)

	result := d.RunRegen(ctx)
	if result.HubsChanged != 1 || result.HubsCreated != 0 || result.HubsFailed != 0 {
		t.Fatalf("hub result = created:%d changed:%d failed:%d, want 0/1/0",
			result.HubsCreated, result.HubsChanged, result.HubsFailed)
	}
	if backend.overwriteID != "hub-biko" {
		t.Fatalf("overwrite id = %q, want hub-biko", backend.overwriteID)
	}
}
