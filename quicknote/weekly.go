package quicknote

import "github.com/barad1tos/noxctl/bear"

// WeeklyDomain handles `#quicknote/weekly` — week-grain entries (retros,
// summaries). Single flat bullet stream master.
var WeeklyDomain = bear.NewFlatListDomain("quicknote/weekly", bear.T("quicknote.weekly.index"))
