package index

import (
	"go/ast"
	"go/parser"
	"go/token"
	"sort"
	"strings"

	"github.com/ArpitK24/forge/internal/ctxengine/symbols"
	"github.com/ArpitK24/forge/internal/ctxengine/tokens"
)

// Chunk kinds beyond the symbol kinds of package symbols.
const (
	// KindHeader is a code file's top-level text outside every symbol:
	// package clause, imports, package-level constants and variables.
	KindHeader = "header"
	// KindWindow is a fixed window of a file we extract no symbols
	// from (prose, config, languages without an extractor yet).
	KindWindow = "window"
)

// WindowLines is the size of a window chunk.
const WindowLines = 60

// Span is a 1-based inclusive line range.
type Span struct{ Start, End int }

// Chunk is the unit the index searches and the packer emits: one
// symbol, a code file's header, or a window of a file without symbols.
type Chunk struct {
	Kind      string // symbols.Kind value, KindHeader or KindWindow
	Name      string // qualified symbol name; "" for header and window
	Start     int    // 1-based, inclusive
	End       int    // 1-based, inclusive
	Signature string // one line: the declaration without its body
	// Exclude lists spans inside [Start, End] that belong to other
	// chunks (a class's methods, or every symbol for the header). They
	// are elided from Text and from the indexed terms.
	Exclude []Span
	Text    string // the chunk's own lines, elisions marked
	Tokens  int    // tokens.Estimate(Text)
}

// IsSymbol reports whether the chunk is a named symbol.
func (c Chunk) IsSymbol() bool { return c.Name != "" }

// SymbolID is the "path::Name" id used by gold sets, or "" for header
// and window chunks.
func (c Chunk) SymbolID(path string) string {
	if c.Name == "" {
		return ""
	}
	return symbols.ID(path, c.Name)
}

// Chunks splits a file into chunks. Go and Python files get one chunk
// per symbol (spans from package symbols, so ids match gold sets) plus
// a header chunk for top-level code; Go signatures come from go/parser
// (the Go fast path). Everything else is cut into WindowLines windows.
func Chunks(path string, src []byte) []Chunk {
	text := string(src)
	lines := strings.Split(text, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	if len(lines) == 0 {
		return nil
	}
	if symbols.Language(path) == "" {
		return windows(lines)
	}
	syms := symbols.Extract(path, src)
	var goSigs map[string]string
	if symbols.Language(path) == "go" {
		goSigs = goSignatures(src)
	}
	var out []Chunk
	var top []Span
	for i, s := range syms {
		c := Chunk{Kind: string(s.Kind), Name: s.Name, Start: s.Start, End: min(s.End, len(lines))}
		nested := false
		for j, o := range syms {
			if j == i {
				continue
			}
			if o.Start >= s.Start && o.End <= s.End && (o.Start > s.Start || o.End < s.End) {
				c.Exclude = append(c.Exclude, Span{o.Start, o.End})
			}
			if s.Start >= o.Start && s.End <= o.End && (s.Start > o.Start || s.End < o.End) {
				nested = true
			}
		}
		c.Exclude = mergeSpans(c.Exclude)
		if !nested {
			top = append(top, Span{s.Start, s.End})
		}
		if sig, ok := goSigs[s.Name]; ok {
			c.Signature = sig
		} else {
			c.Signature = firstCodeLine(lines, c.Start, c.End)
		}
		c.Text = render(lines, c.Start, c.End, c.Exclude)
		c.Tokens = tokens.Estimate(c.Text)
		out = append(out, c)
	}
	h := Chunk{Kind: KindHeader, Start: 1, End: len(lines), Exclude: mergeSpans(top)}
	if hasCode(lines, h.Exclude) {
		h.Signature = firstCodeLine(lines, 1, len(lines))
		h.Text = render(lines, 1, len(lines), h.Exclude)
		h.Tokens = tokens.Estimate(h.Text)
		out = append([]Chunk{h}, out...)
	}
	return out
}

func windows(lines []string) []Chunk {
	var out []Chunk
	for start := 1; start <= len(lines); start += WindowLines {
		end := min(start+WindowLines-1, len(lines))
		if !hasCode(lines[start-1:end], nil) {
			continue
		}
		c := Chunk{Kind: KindWindow, Start: start, End: end}
		c.Signature = firstCodeLine(lines, start, end)
		c.Text = render(lines, start, end, nil)
		c.Tokens = tokens.Estimate(c.Text)
		out = append(out, c)
	}
	return out
}

// render joins lines [start, end] (1-based) and replaces each excluded
// span with a single elision line.
func render(lines []string, start, end int, exclude []Span) string {
	var b strings.Builder
	ex := 0
	for ln := start; ln <= end && ln <= len(lines); ln++ {
		for ex < len(exclude) && exclude[ex].End < ln {
			ex++
		}
		if ex < len(exclude) && exclude[ex].Start <= ln {
			if ln == exclude[ex].Start {
				b.WriteString(indentOf(lines[ln-1]))
				b.WriteString("...\n")
			}
			continue
		}
		b.WriteString(lines[ln-1])
		b.WriteByte('\n')
	}
	return b.String()
}

func indentOf(s string) string {
	return s[:len(s)-len(strings.TrimLeft(s, " \t"))]
}

func hasCode(lines []string, exclude []Span) bool {
	ex := 0
	for i, l := range lines {
		ln := i + 1
		for ex < len(exclude) && exclude[ex].End < ln {
			ex++
		}
		if ex < len(exclude) && exclude[ex].Start <= ln {
			continue
		}
		if strings.TrimSpace(l) != "" {
			return true
		}
	}
	return false
}

// firstCodeLine is the generic signature: the first line in range that
// is not blank, a comment or a decorator, trimmed and capped.
func firstCodeLine(lines []string, start, end int) string {
	for ln := start; ln <= end && ln <= len(lines); ln++ {
		t := strings.TrimSpace(lines[ln-1])
		if t == "" || strings.HasPrefix(t, "//") || strings.HasPrefix(t, "#") ||
			strings.HasPrefix(t, "@") || strings.HasPrefix(t, "/*") || strings.HasPrefix(t, "*") {
			continue
		}
		return capLine(t)
	}
	return ""
}

func capLine(s string) string {
	const maxSig = 200
	if len(s) > maxSig {
		return s[:maxSig] + "..."
	}
	return s
}

func mergeSpans(sp []Span) []Span {
	if len(sp) == 0 {
		return nil
	}
	sort.Slice(sp, func(i, j int) bool { return sp[i].Start < sp[j].Start })
	out := []Span{sp[0]}
	for _, s := range sp[1:] {
		last := &out[len(out)-1]
		if s.Start <= last.End+1 {
			last.End = max(last.End, s.End)
			continue
		}
		out = append(out, s)
	}
	return out
}

// goSignatures is the Go fast path: one go/parser pass (no type
// checking) yielding each declaration's signature without its body,
// keyed by the same qualified names package symbols uses.
func goSignatures(src []byte) map[string]string {
	fset := token.NewFileSet()
	f, _ := parser.ParseFile(fset, "", src, parser.SkipObjectResolution)
	if f == nil {
		return nil
	}
	slice := func(from, to token.Pos) string {
		a, b := fset.Position(from).Offset, fset.Position(to).Offset
		if a < 0 || b > len(src) || a >= b {
			return ""
		}
		return capLine(strings.Join(strings.Fields(string(src[a:b])), " "))
	}
	out := map[string]string{}
	for _, d := range f.Decls {
		switch d := d.(type) {
		case *ast.FuncDecl:
			name := d.Name.Name
			if d.Recv != nil && len(d.Recv.List) > 0 {
				if r := recvName(d.Recv.List[0].Type); r != "" {
					name = r + "." + name
				}
			}
			end := d.End()
			if d.Body != nil {
				end = d.Body.Lbrace
			}
			out[name] = slice(d.Type.Func, end)
		case *ast.GenDecl:
			if d.Tok != token.TYPE {
				continue
			}
			for _, sp := range d.Specs {
				ts, ok := sp.(*ast.TypeSpec)
				if !ok {
					continue
				}
				end := ts.End()
				switch t := ts.Type.(type) {
				case *ast.StructType:
					end = t.Fields.Opening + 1
				case *ast.InterfaceType:
					end = t.Methods.Opening + 1
				}
				out[ts.Name.Name] = "type " + slice(ts.Name.Pos(), end)
			}
		}
	}
	return out
}

func recvName(e ast.Expr) string {
	for {
		switch t := e.(type) {
		case *ast.StarExpr:
			e = t.X
		case *ast.ParenExpr:
			e = t.X
		case *ast.IndexExpr:
			e = t.X
		case *ast.IndexListExpr:
			e = t.X
		case *ast.Ident:
			return t.Name
		default:
			return ""
		}
	}
}
