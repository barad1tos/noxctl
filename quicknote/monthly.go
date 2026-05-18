package quicknote

import "github.com/barad1tos/noxctl/bear"

// MonthlyDomain handles `#quicknote/monthly` — month-grain entries (monthly
// retros, planning). Single flat bullet stream master.
var MonthlyDomain = bear.NewFlatListDomain("quicknote/monthly", bear.T("quicknote.monthly.index"))
