package config

// daemonStanza mirrors the `[daemon]` + `[daemon.paths]` TOML schema.
// Pointer types so per-field provenance can distinguish "field absent"
// from "field explicitly zero" — needed for file-source vs default
// reporting.
type daemonStanza struct {
	DebouncePause  *string `toml:"debounce_pause" json:"debounce_pause,omitempty"`
	MaxBurstWindow *string `toml:"max_burst_window" json:"max_burst_window,omitempty"`
	AuditEnabled   *bool   `toml:"audit_enabled" json:"audit_enabled,omitempty"`
	// Operator-tuned bearcli pool cap.
	BearcliConcurrency *int `toml:"bearcli_concurrency" json:"bearcli_concurrency,omitempty"`
	// 30s default, "0s" disables polling, negative is fatal.
	MtimePollInterval *string `toml:"mtime_poll_interval" json:"mtime_poll_interval,omitempty"`
	// 2s default, "0s" disables fast-pass, negative is fatal.
	AutoTagPollInterval *string `toml:"auto_tag_poll_interval" json:"auto_tag_poll_interval,omitempty"`
	// Universal fast-pass canonicalization kill-switch (default true).
	DomainBootstrap *bool              `toml:"domain_bootstrap" json:"domain_bootstrap,omitempty"`
	Paths           *daemonPathsStanza `toml:"paths" json:"paths,omitempty"`
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
