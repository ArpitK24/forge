package symbols

import (
	"bytes"
	"regexp"
)

var (
	pyDefRe   = regexp.MustCompile(`^(?:async\s+)?def\s+([A-Za-z_][A-Za-z0-9_]*)`)
	pyClassRe = regexp.MustCompile(`^class\s+([A-Za-z_][A-Za-z0-9_]*)`)
)

type pyBlock struct {
	sym    Symbol
	indent int
	isFunc bool
}

// extractPython is a cgo-free scanner for Python definitions. It tracks
// triple-quoted strings, bracket nesting and backslash continuations so
// that only logical-line starts can open or close a block, then closes
// blocks on dedent. Decorators are included in the symbol's span.
// Functions nested inside functions are folded into their parent.
func extractPython(src []byte) []Symbol {
	lines := bytes.Split(src, []byte("\n"))
	var (
		out        []Symbol
		stack      []pyBlock
		lx         pyLexer
		lastCode   int // last line that held code (not blank, not comment-only)
		decorStart int // first line of a pending decorator run, 0 if none
	)
	closeTo := func(indent int) {
		for len(stack) > 0 && stack[len(stack)-1].indent >= indent {
			b := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			b.sym.End = max(lastCode, b.sym.Start)
			out = append(out, b.sym)
		}
	}
	for i, raw := range lines {
		ln := i + 1
		line := bytes.TrimRight(raw, "\r")
		logicalStart := lx.atLogicalStart()
		stripped := bytes.TrimLeft(line, " \t\f")
		if !logicalStart {
			if len(bytes.TrimSpace(line)) > 0 {
				lastCode = ln
			}
			lx.scan(line)
			continue
		}
		if len(stripped) == 0 || stripped[0] == '#' {
			continue
		}
		indent := pyIndent(line)
		closeTo(indent)
		switch {
		case stripped[0] == '@':
			if decorStart == 0 {
				decorStart = ln
			}
		case pyDefRe.Match(stripped) || pyClassRe.Match(stripped):
			isFunc := pyDefRe.Match(stripped)
			var name string
			if isFunc {
				name = string(pyDefRe.FindSubmatch(stripped)[1])
			} else {
				name = string(pyClassRe.FindSubmatch(stripped)[1])
			}
			start := ln
			if decorStart != 0 {
				start = decorStart
			}
			decorStart = 0
			var parent *pyBlock
			if len(stack) > 0 {
				parent = &stack[len(stack)-1]
			}
			if parent != nil && parent.isFunc {
				break // nested in a function: part of the parent
			}
			kind := KindClass
			if isFunc {
				kind = KindFunc
				if parent != nil {
					kind = KindMethod
				}
			}
			if parent != nil {
				name = parent.sym.Name + "." + name
			}
			stack = append(stack, pyBlock{
				sym:    Symbol{Name: name, Kind: kind, Start: start},
				indent: indent,
				isFunc: isFunc,
			})
		default:
			decorStart = 0
		}
		lastCode = ln
		lx.scan(line)
	}
	closeTo(-1)
	return out
}

func pyIndent(line []byte) int {
	n := 0
	for _, c := range line {
		switch c {
		case ' ':
			n++
		case '\t':
			n = (n/8 + 1) * 8
		case '\f':
			n = 0
		default:
			return n
		}
	}
	return n
}

// pyLexer carries the cross-line lexical state that decides whether a
// physical line starts a new logical line.
type pyLexer struct {
	triple byte // quote char of an open triple-quoted string, 0 if none
	depth  int  // open (, [, { count
	cont   bool // previous line ended in a backslash continuation
}

func (l *pyLexer) atLogicalStart() bool { return l.triple == 0 && l.depth == 0 && !l.cont }

func (l *pyLexer) scan(line []byte) {
	l.cont = false
	i := 0
	for i < len(line) {
		c := line[i]
		if l.triple != 0 {
			if c == '\\' {
				i += 2
				continue
			}
			if c == l.triple && i+2 < len(line) && line[i+1] == c && line[i+2] == c {
				l.triple = 0
				i += 3
				continue
			}
			i++
			continue
		}
		switch c {
		case '#':
			return
		case '\'', '"':
			if i+2 < len(line) && line[i+1] == c && line[i+2] == c {
				l.triple = c
				i += 3
				continue
			}
			// Single-line string: skip to its closing quote.
			j := i + 1
			for j < len(line) {
				if line[j] == '\\' {
					j += 2
					continue
				}
				if line[j] == c {
					break
				}
				j++
			}
			i = j + 1
			continue
		case '(', '[', '{':
			l.depth++
		case ')', ']', '}':
			if l.depth > 0 {
				l.depth--
			}
		case '\\':
			if i == len(line)-1 {
				l.cont = true
			}
			i += 2
			continue
		}
		i++
	}
}
