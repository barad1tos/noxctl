package quicknote

import "github.com/barad1tos/noxctl/bear"

// DecadalDomain handles `#quicknote/decadal` — terminal bucket for
// notes that aged out of yearly. Once an atom lands here time-based
// promotion never moves it again.
var DecadalDomain = bear.NewFlatListDomain("quicknote/decadal", bear.T("quicknote.decadal.index"))
