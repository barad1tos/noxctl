package domain

// IsAuxNote reports whether a note is an auto-generated master or hub (true)
// versus an operator-authored atom (false).
func IsAuxNote(d *Domain, n Note) bool {
	return d.skipNote(n)
}
