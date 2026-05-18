package it

import "github.com/barad1tos/noxctl/bear"

// TechnologiesDomain handles `it/technologies` with the same flat-table
// shape as VendorsDomain. Atomics backlink at the master `✱ IT Технології`;
// the master groups them in fixed columns by technology (kubernetes,
// linux, python, articles,...). One click from master to atomic, no
// Tier-2 hub layer.
//
// Atomic canonical header:
//
//	#it/technologies | [[✱ IT Технології]] | <tech>
//
// where `<tech>` is the bucket name. The "articles" column holds the
// generic IaC/DevOps writing that's neither a specific technology nor
// vendor-bound (the migration parked the legacy `it/technologies` notes
// — Grafana, KISS vs DRY, Terraform Tips — there). UnknownBucket points
// at "articles" too, so atomics dropped into the tag without a canonical
// 3rd segment land in the generic column instead of an Інше overflow.
var TechnologiesDomain = bear.NewGroupedVerticalFlatDomain(
	"it/technologies",
	bear.T("it.technologies.index"),
	"articles",
	[]string{"kubernetes", "linux", "python", "articles"},
)
