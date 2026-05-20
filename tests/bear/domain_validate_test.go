package bear_test

import (
	"strings"
	"testing"

	"github.com/barad1tos/noxctl/bear/domain"
	"github.com/barad1tos/noxctl/bear/render"
)

// TestDomainValidate_UmbrellaRequiresDefaultChild covers spec component
// 4's validation contract: every umbrella domain must declare a
// DefaultChild whose value matches one of its registered (non-umbrella)
// children. Without this rule, umbrella masters fall back to hardcoded
// children[0] — fragile across registration-order refactors.
func TestDomainValidate_UmbrellaRequiresDefaultChild(t *testing.T) {
	leafA := &domain.Domain{
		Tag:          "library/poetry",
		CanonicalTag: "#library/poetry",
		IndexTitle:   "✱ Поезія",
		ParseMeta:    render.DefaultParseMetaCanonical,
		RenderMaster: render.DefaultRenderMasterFlat,
	}
	leafB := &domain.Domain{
		Tag:          "library/lyrics",
		CanonicalTag: "#library/lyrics",
		IndexTitle:   "✱ Lyrics",
		ParseMeta:    render.DefaultParseMetaCanonical,
		RenderMaster: render.DefaultRenderMasterFlat,
	}

	cases := []struct {
		name         string
		defaultChild string
		children     []*domain.Domain
		isUmbrella   bool
		wantErr      string // substring; "" = expect no error
	}{
		{
			name:         "umbrella with empty DefaultChild rejected",
			defaultChild: "",
			children:     []*domain.Domain{leafA, leafB},
			isUmbrella:   true,
			wantErr:      "DefaultChild is required",
		},
		{
			name:         "umbrella with unknown DefaultChild rejected",
			defaultChild: "library/nonsense",
			children:     []*domain.Domain{leafA, leafB},
			isUmbrella:   true,
			wantErr:      "library/nonsense",
		},
		{
			name:         "umbrella with valid DefaultChild accepted",
			defaultChild: "library/poetry",
			children:     []*domain.Domain{leafA, leafB},
			isUmbrella:   true,
			wantErr:      "",
		},
		{
			name:         "non-umbrella ignores DefaultChild entirely",
			defaultChild: "library/anything",
			isUmbrella:   false,
			wantErr:      "",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := buildValidateCase(t, c.isUmbrella, c.defaultChild, c.children)
			assertValidateError(t, d.Validate(), c.wantErr)
		})
	}
}

// buildValidateCase assembles the Domain under test, either via the
// umbrella factory (which surfaces factory errors through Validate) or
// as a standalone leaf literal carrying an arbitrary DefaultChild.
func buildValidateCase(t *testing.T, isUmbrella bool, defaultChild string, children []*domain.Domain) *domain.Domain {
	t.Helper()
	if isUmbrella {
		return render.NewUmbrellaDomainForTest(t, "library", "✱ Library", defaultChild, children)
	}
	return &domain.Domain{
		Tag:          "library/poetry-leaf",
		CanonicalTag: "#library/poetry-leaf",
		IndexTitle:   "✱ Poetry Leaf",
		DefaultChild: defaultChild,
		ParseMeta:    render.DefaultParseMetaCanonical,
		RenderMaster: render.DefaultRenderMasterFlat,
	}
}

// assertValidateError checks that Validate's error matches the case
// expectation: wantErr=="" means nil; otherwise err must contain it.
func assertValidateError(t *testing.T, err error, wantErr string) {
	t.Helper()
	if wantErr == "" {
		if err != nil {
			t.Errorf("Validate() = %v, want nil", err)
		}
		return
	}
	if err == nil || !strings.Contains(err.Error(), wantErr) {
		t.Errorf("Validate() = %v, want error containing %q", err, wantErr)
	}
}

// TestNewUmbrellaDomain_RejectsNestedUmbrella covers the cross-domain
// rule: DefaultChild must point at a NON-umbrella child. A nested
// umbrella would cascade into recursion / unclear semantics. The factory
// itself enforces this via the error-returning newUmbrellaDomainStrict
// path (NewUmbrellaDomainForTest surfaces it as Validate error).
func TestNewUmbrellaDomain_RejectsNestedUmbrella(t *testing.T) {
	innerUmbrella := &domain.Domain{
		Tag:             "library/sub-umbrella",
		CanonicalTag:    "#library/sub-umbrella",
		IndexTitle:      "✱ Sub",
		SkipAtomicsPass: true,
		DefaultChild:    "library/poetry",
		ParseMeta:       render.DefaultParseMetaCanonical,
		RenderMaster:    render.DefaultRenderMasterFlat,
	}
	d := render.NewUmbrellaDomainForTest(t, "library", "✱ Library", "library/sub-umbrella",
		[]*domain.Domain{innerUmbrella})

	err := d.Validate()
	if err == nil || !strings.Contains(err.Error(), "nested umbrella") {
		t.Errorf("Validate() = %v, want error containing 'nested umbrella'", err)
	}
}
