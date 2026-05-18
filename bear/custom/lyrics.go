package custom

import (
	"fmt"

	"github.com/barad1tos/noxctl/bear"
)

// init installs the "lyrics" renderer into the closed registry. The
// register-from-init pattern matches the rest of bear/custom/<name>.go
// — Go guarantees same-package init runs before cross-package var
// init (i18n.go's init populates the catalog first), so dispatch can
// Lookup("lyrics") at any point after package import.
func init() { RegisterMaster("lyrics", renderLyricsMaster) }

// renderLyricsMaster produces ✱ Тексти as two vertical sections — Іноземні
// for Latin-named artists, СНГ for Cyrillic — each a bullet list of per-
// artist Tier-2 hub wikilinks, alphabetised. Empty sections are dropped.
//
//	# ✱ Тексти
//	#library/lyrics
//	---
//
//	## Іноземні (138)
//	- [[A Perfect Circle]]
//	- [[Abney Park]]
//	-...
//
//	## СНГ (25)
//	- [[Адаптация]]
//	-...
func renderLyricsMaster(d *bear.Domain, groups map[string][]bear.Note) string {
	latin, cyrillic := splitArtistsByAlphabet(groups, d.UnknownBucket)
	sections := make([]bear.Section, 0, 2)
	sections = appendArtistSection(sections, bear.T("library.lyrics.section.foreign"), latin)
	sections = appendArtistSection(sections, bear.T("library.lyrics.section.cis"), cyrillic)
	return bear.RenderVerticalSections(d, sections)
}

// appendArtistSection appends a Section with the given label + artist
// bullets when artists is non-empty; returns sections unchanged otherwise.
// Bullet text is plain `[[Artist]]` — hub names are unique by daemon
// construction, so no URL-form disambiguation is needed at master level.
func appendArtistSection(sections []bear.Section, label string, artists []string) []bear.Section {
	if len(artists) == 0 {
		return sections
	}
	bullets := make([]string, len(artists))
	for index, artist := range artists {
		bullets[index] = "[[" + artist + "]]"
	}
	return append(sections, bear.Section{
		Header:  fmt.Sprintf("%s (%d)", label, len(artists)),
		Bullets: bullets,
	})
}

// splitArtistsByAlphabet partitions bucket keys by the first-letter group
// from bear.FirstLetter: group "1" (Latin) → Іноземні, groups "2"/"3"/"4"
// (Cyrillic and other scripts) → СНГ. Each list is alphabetised within its
// group. The unknownBucket is excluded — it has no artist identity.
func splitArtistsByAlphabet(groups map[string][]bear.Note, unknownBucket string) (latin, cyrillic []string) {
	for artist := range groups {
		if artist == unknownBucket {
			continue
		}
		group, _ := bear.FirstLetter(artist)
		if group == "1" {
			latin = append(latin, artist)
		} else {
			cyrillic = append(cyrillic, artist)
		}
	}
	bear.SortTitles(latin)
	bear.SortTitles(cyrillic)
	return latin, cyrillic
}
