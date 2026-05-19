// Package config — uniform error formatter for Load failures.
//
// FormatLoadError is the single shim cmd/noxctl/validate.go uses to
// reshape every Load error into the contract:
//
//	path:line:col: kind: message
//
// where kind ∈ {parse, type-mismatch, unknown-field, validate}. The
// classifier mirrors loader.go::decodeStrict's errors.AsType cascade
// so the output stays consistent whether the underlying failure came
// from BurntSushi/toml's lexer, type-checker, the metadata.Undecoded
// strict-decode pass, or the post-decode ValidateCatalog / Dispatch
// chain. errors.Join'd aggregates fan out one line per leaf error.
package config

import (
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/BurntSushi/toml"
)

// typeMismatchRE pulls the line number out of BurntSushi/toml's
// type-mismatch error string. Format observed v1.6.0:
//
//	"toml: line 2 (last key \"meta.version\"): incompatible types:..."
var typeMismatchRE = regexp.MustCompile(`line (\d+) \(last key "([^"]+)"\)`)

// wrappedParseRE detects loader.go::decodeStrict's pre-formatted
// parse-error string. decodeStrict wraps toml.ParseError via
// fmt.Errorf("%s:%d:%d: %s", path, line, col, pe.Error) (NOT %w) so
// errors.As(*toml.ParseError) cannot reach the typed instance — we
// pattern-match on the synthesized prefix instead. Captures line+col
// for the uniform-shape replay.
var wrappedParseRE = regexp.MustCompile(`^.+:(\d+):(\d+): toml: line \d+:`)

// FormatLoadError classifies err and returns one or more lines of
// uniform shape:
//
//	path:line:col: kind: message
//
// Kinds:
//
//	parse — toml.ParseError (true syntax errors; carries line+col)
//	type-mismatch — wrong TOML type for a field; carries line, col=0
//	unknown-field — strict-decode unknown key; line=0, col=0
//	validate — anything else from validate/dispatch.
//
// errors.Join'd aggregates fan out one line per leaf error. Empty err
// returns the empty string.
func FormatLoadError(err error, path string) string {
	if err == nil {
		return ""
	}
	leaves := flattenJoinedErrors(err)
	lines := make([]string, 0, len(leaves))
	for _, leaf := range leaves {
		lines = append(lines, classifyOne(leaf, path))
	}
	return strings.Join(lines, "\n")
}

// flattenJoinedErrors recursively unwraps errors.Join'd aggregates into
// a flat slice of leaf errors. Recognizes the stdlib joined-error
// interface { Unwrap []error }. Plain wrapped errors return as-is.
func flattenJoinedErrors(err error) []error {
	type unwrapper interface{ Unwrap() []error }
	if u, ok := err.(unwrapper); ok {
		var out []error
		for _, child := range u.Unwrap() {
			out = append(out, flattenJoinedErrors(child)...)
		}
		return out
	}
	return []error{err}
}

// classifyOne formats a single leaf error in uniform shape. Cascade:
//
// 1. errors.As(*toml.ParseError) → parse (typed, raw from BurntSushi)
// 2. wrappedParseRE prefix match → parse (pre-formatted by decodeStrict)
// 3. typeMismatchRE → type-mismatch
// 4. substring ": unknown field " → unknown-field
// 5. fall through → validate
func classifyOne(err error, path string) string {
	if pe, ok := errors.AsType[toml.ParseError](err); ok {
		return fmt.Sprintf("%s:%d:%d: parse: %s",
			path, pe.Position.Line, pe.Position.Col, pe.Error())
	}
	msg := err.Error()
	body := strings.TrimPrefix(msg, path+": ")
	if m := wrappedParseRE.FindStringSubmatch(msg); m != nil {
		// Strip decodeStrict's synthesized "path:line:col: " prefix so
		// the body field carries only the toml: substring once.
		stripped := strings.TrimPrefix(msg, fmt.Sprintf("%s:%s:%s: ", path, m[1], m[2]))
		return fmt.Sprintf("%s:%s:%s: parse: %s", path, m[1], m[2], stripped)
	}
	if m := typeMismatchRE.FindStringSubmatch(msg); m != nil {
		return fmt.Sprintf("%s:%s:0: type-mismatch: %s", path, m[1], body)
	}
	if strings.Contains(msg, ": unknown field ") {
		return fmt.Sprintf("%s:0:0: unknown-field: %s", path, body)
	}
	return fmt.Sprintf("%s:0:0: validate: %s", path, body)
}
