package config

// daemonStanza mirrors the `[daemon]` + `[daemon.paths]` TOML schema.
// Pointer types per D-09: distinguishes "field absent" from "field
// explicitly zero", which the spec requires so per-field provenance
// can distinguish file-source from default.
type daemonStanza struct {
	DebouncePause       *string `toml:"debounce_pause"`
	MaxBurstWindow      *string `toml:"max_burst_window"`
	AuditEnabled        *bool   `toml:"audit_enabled"`
	BearcliConcurrency  *int    `toml:"bearcli_concurrency"`    //  D-01: operator-tuned bearcli pool cap.
	MtimePollInterval   *string `toml:"mtime_poll_interval"`    //  POLL-01: 30s default, "0s" disables, negative is fatal.
	AutoTagPollInterval *string `toml:"auto_tag_poll_interval"` //  TAG-01: 2s default, "0s" disables fast-pass, negative is fatal.
	//: universal fast-pass canonicalization kill-switch
	// (default true). ships the flag; /3 wires the pass.
	DomainBootstrap *bool              `toml:"domain_bootstrap"`
	Paths           *daemonPathsStanza `toml:"paths"`
}

// daemonPathsStanza mirrors the `[daemon.paths]` sub-table.
type daemonPathsStanza struct {
	State  *string `toml:"state"`
	Lock   *string `toml:"lock"`
	Pins   *string `toml:"pins"`
	Log    *string `toml:"log"`
	BearDB *string `toml:"bear_db"`
}

// daemonFileContents wraps the top-level stanza so toml.Decode can
// reach [daemon] via the struct path Daemon -> fields.
type daemonFileContents struct {
	Daemon daemonStanza `toml:"daemon"`
}
