package quicknote

import "github.com/barad1tos/noxctl/bear"

// YearlyDomain handles `#quicknote/yearly` — year-grain entries that
// rolled out of monthly when the calendar year crossed. Single flat
// bullet stream master, identical shape to its siblings.
var YearlyDomain = bear.NewFlatListDomain("quicknote/yearly", bear.T("quicknote.yearly.index"))
