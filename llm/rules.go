package llm

import "github.com/barad1tos/noxctl/bear"

// RulesDomain handles llm/rules with the same flat-list shape as
// characters. Rules notes are short prescriptions; per-rule hubs would
// be empty navigation overhead.
var RulesDomain = bear.NewFlatListDomain("llm/rules", bear.T("llm.rules.index"))
