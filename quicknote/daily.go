// Package quicknote holds the `#quicknote` umbrella tag's child domains —
// daily, weekly, monthly buckets for ad-hoc capture. Notes a user creates
// without an explicit tag (Bear's compose-from-Notes-view) are auto-stamped
// with `#quicknote/daily` by bear.ApplyDailyDefaultTag, parking them in the
// daily list until the user re-tags via cmd+x bullet move from the master.
package quicknote

import "github.com/barad1tos/noxctl/bear"

// DailyDomain handles `#quicknote/daily` — single flat bullet stream, no
// per-bucket sub-grouping. Bear's auto-titling (timestamp-based) names new
// notes; the master alphabetises them.
//
// QuickPlaceholderH1="Quicknote" opts into the x-callback bootstrap URL
// pattern: master "Нова нотатка" links create notes with H1="# Quicknote"
// as a marker. The daemon's fast-pass swaps that marker for a fresh
// timestamp within ≤2 s of click.
var DailyDomain *bear.Domain

func init() {
	DailyDomain = bear.NewFlatListDomain("quicknote/daily", bear.T("quicknote.daily.index"))
	DailyDomain.QuickPlaceholderH1 = "Quicknote"
}
