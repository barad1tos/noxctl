package it

import "github.com/barad1tos/noxctl/bear"

// VendorsDomain handles `it/vendors` with a flat-table master: each atomic
// backlinks at the master `✱ IT Vendors`, no Tier-2 hubs, and the table
// groups atomics by vendor in fixed columns. At the corpus's current scale
// (1-3 atomics per vendor) per-bucket hubs would only add navigation
// overhead — flat-table is one click from master to atomic.
//
// Atomic canonical header:
//
//	#it/vendors | [[✱ IT Vendors]] | <vendor>
//
// where `<vendor>` is the bucket name (apple, aws, mikrotik, windows).
// Adding a new vendor needs no code change — register it in the column
// list to fix its position, otherwise it lands in "Інше" alphabetically.
//
// Bidirectional master via bear.ParseMasterTable: cut a `[[Title]]` bullet
// from one column, paste into another, save → on the next regen the
// daemon rewrites the matching atomic's canonical bucket.
var VendorsDomain = bear.NewGroupedVerticalFlatDomain(
	"it/vendors",
	"✱ IT Vendors",
	bear.T("common.unknown.other"), // unknown bucket: safety net for atomics without a 3rd segment
	[]string{"apple", "aws", "mikrotik", "windows"},
)
