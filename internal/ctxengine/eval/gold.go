package eval

import (
	"path"
	"sort"
	"strings"

	"github.com/ArpitK24/forge/internal/ctxengine/symbols"
)

// GoldOptions controls gold extraction.
type GoldOptions struct {
	// IncludeTests keeps test files in the gold set. Off by default:
	// SWE-bench's gold patch excludes its tests already, and for mined
	// tasks the tests are the evidence, not the thing to retrieve.
	IncludeTests bool
}

// IsTestPath reports whether p looks like a test file in Go or Python
// (or lives under a tests/, testdata/ or __tests__/ directory). It is
// deliberately narrow: directories such as django/test/ hold library
// code. SWE-bench needs no filtering at all because its gold patch
// already excludes the test patch.
func IsTestPath(p string) bool {
	base := path.Base(p)
	if strings.HasSuffix(base, "_test.go") {
		return true
	}
	if strings.HasSuffix(base, ".py") &&
		(strings.HasPrefix(base, "test_") || strings.HasSuffix(base, "_test.py") ||
			base == "conftest.py") {
		return true
	}
	for _, seg := range strings.Split(path.Dir(p), "/") {
		switch seg {
		case "tests", "testdata", "__tests__":
			return true
		}
	}
	return false
}

// GoldFiles returns the base-side files a patch changes (sorted) and the
// files it creates. Test files are dropped unless IncludeTests is set.
func GoldFiles(diffs []FileDiff, opt GoldOptions) (gold, added []string) {
	g, a := map[string]bool{}, map[string]bool{}
	for _, d := range diffs {
		if !opt.IncludeTests && IsTestPath(d.Path()) {
			continue
		}
		if d.Created() {
			a[d.NewPath] = true
		} else {
			g[d.OldPath] = true
		}
	}
	return sortedKeys(g), sortedKeys(a)
}

// GoldFunctions maps every changed or inserted base-side line of the
// gold files to its innermost enclosing symbol (symbols.Extract) and
// returns the sorted, unique "path::Symbol" ids. read returns a file's
// content at the base commit. Lines at package/module level have no
// symbol and contribute nothing; files in unsupported languages are
// skipped.
func GoldFunctions(diffs []FileDiff, opt GoldOptions, read func(path string) ([]byte, bool)) []string {
	ids := map[string]bool{}
	for _, d := range diffs {
		if d.Created() || d.Binary || symbols.Language(d.OldPath) == "" {
			continue
		}
		if !opt.IncludeTests && IsTestPath(d.OldPath) {
			continue
		}
		src, ok := read(d.OldPath)
		if !ok {
			continue
		}
		syms := symbols.Extract(d.OldPath, src)
		for _, ln := range d.Changed {
			if s, ok := symbols.Enclosing(syms, ln); ok {
				ids[symbols.ID(d.OldPath, s.Name)] = true
			}
		}
		for _, after := range d.InsertAfter {
			if s, ok := symbols.EnclosingInsertion(syms, after); ok {
				ids[symbols.ID(d.OldPath, s.Name)] = true
			}
		}
	}
	return sortedKeys(ids)
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
