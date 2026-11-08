// Package symbols extracts named code symbols (functions, methods,
// classes, types) with their line spans, and splits identifiers into
// search terms. It has no dependencies outside the standard library so
// that both gold extraction and retrievers agree on what a "symbol" is.
package symbols

import (
	"path"
	"sort"
	"strings"
)

// Kind classifies a symbol.
type Kind string

// Symbol kinds.
const (
	KindFunc   Kind = "func"
	KindMethod Kind = "method"
	KindType   Kind = "type"
	KindClass  Kind = "class"
)

// Symbol is a named definition with a 1-based, inclusive line span.
//
// Name is qualified within its file: Go methods are "Recv.Method",
// Python methods are "Class.method", nested classes "Outer.Inner".
// Functions nested inside functions are not separate symbols; their
// lines belong to the enclosing function.
type Symbol struct {
	Name  string `json:"name"`
	Kind  Kind   `json:"kind"`
	Start int    `json:"start"`
	End   int    `json:"end"`
}

// Language reports the language Extract uses for a path, or "" when the
// file is not one we extract symbols from.
func Language(p string) string {
	switch strings.ToLower(path.Ext(p)) {
	case ".go":
		return "go"
	case ".py", ".pyi":
		return "python"
	}
	return ""
}

// Extract returns the symbols defined in src, sorted by start line then
// name. Unsupported languages and unparsable Go files yield nil (Go
// files fall back to a partial parse when possible).
func Extract(p string, src []byte) []Symbol {
	var out []Symbol
	switch Language(p) {
	case "go":
		out = extractGo(src)
	case "python":
		out = extractPython(src)
	default:
		return nil
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Start != out[j].Start {
			return out[i].Start < out[j].Start
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// Enclosing returns the innermost symbol whose span contains line, or
// false when the line is at module/package level.
func Enclosing(syms []Symbol, line int) (Symbol, bool) {
	best := -1
	for i, s := range syms {
		if s.Start <= line && line <= s.End {
			if best < 0 || spanLen(s) < spanLen(syms[best]) ||
				(spanLen(s) == spanLen(syms[best]) && s.Start > syms[best].Start) {
				best = i
			}
		}
	}
	if best < 0 {
		return Symbol{}, false
	}
	return syms[best], true
}

// EnclosingInsertion returns the innermost symbol that strictly contains
// an insertion made after line `after` (0 means before the first line):
// the symbol must start at or before `after` and end after it, so text
// appended right after a closing line does not count as inside.
func EnclosingInsertion(syms []Symbol, after int) (Symbol, bool) {
	best := -1
	for i, s := range syms {
		if s.Start <= after && after < s.End {
			if best < 0 || spanLen(s) < spanLen(syms[best]) {
				best = i
			}
		}
	}
	if best < 0 {
		return Symbol{}, false
	}
	return syms[best], true
}

func spanLen(s Symbol) int { return s.End - s.Start }

// ID returns the canonical "path::Name" identifier used in gold sets and
// ranked symbol lists.
func ID(p, name string) string { return p + "::" + name }
