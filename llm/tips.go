package llm

import "github.com/barad1tos/noxctl/bear"

// TipsDomain handles llm/tips with the same flat-list shape. Tips are
// terse one-off notes; bucketing is unnecessary at this scale.
var TipsDomain = bear.NewFlatListDomain("llm/tips", bear.T("llm.tips.index"))
