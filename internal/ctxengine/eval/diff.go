package eval

import (
	"bufio"
	"fmt"
	"strconv"
	"strings"
)

// FileDiff is one file's section of a unified diff, reduced to what
// gold extraction needs.
type FileDiff struct {
	OldPath string // "" when the file is created
	NewPath string // "" when the file is deleted
	// Changed are base-side (old) line numbers that were removed or
	// replaced.
	Changed []int
	// InsertAfter are base-side positions where lines were added:
	// k means "after old line k" (0 = top of file).
	InsertAfter []int
	Binary      bool
}

// Created reports whether the diff creates the file.
func (d FileDiff) Created() bool { return d.OldPath == "" }

// Path is the file's base-side path, or its new path when created.
func (d FileDiff) Path() string {
	if d.OldPath != "" {
		return d.OldPath
	}
	return d.NewPath
}

// ParseUnifiedDiff parses `git diff` style output. Hunks are consumed by
// their line counts, so content lines that look like headers are never
// misread.
func ParseUnifiedDiff(patch string) ([]FileDiff, error) {
	var out []FileDiff
	var cur *FileDiff
	flush := func() {
		if cur != nil {
			out = append(out, *cur)
			cur = nil
		}
	}
	sc := bufio.NewScanner(strings.NewReader(patch))
	sc.Buffer(make([]byte, 0, 1<<20), 64<<20)
	oldLeft, newLeft, oldLine := 0, 0, 0
	for sc.Scan() {
		line := strings.TrimSuffix(sc.Text(), "\r")
		if cur != nil && (oldLeft > 0 || newLeft > 0) {
			switch {
			case strings.HasPrefix(line, "\\"):
				continue // "\ No newline at end of file"
			case strings.HasPrefix(line, "-"):
				cur.Changed = append(cur.Changed, oldLine)
				oldLine++
				oldLeft--
			case strings.HasPrefix(line, "+"):
				cur.InsertAfter = append(cur.InsertAfter, oldLine-1)
				newLeft--
			default: // context (" " or an empty line from a trimmed editor)
				oldLine++
				oldLeft--
				newLeft--
			}
			continue
		}
		switch {
		case strings.HasPrefix(line, "diff --git "):
			flush()
			a, b := splitGitHeader(strings.TrimPrefix(line, "diff --git "))
			cur = &FileDiff{OldPath: a, NewPath: b}
		case cur == nil:
			// Preamble (e.g. a commit message) before the first file.
		case strings.HasPrefix(line, "new file mode"):
			cur.OldPath = ""
		case strings.HasPrefix(line, "deleted file mode"):
			cur.NewPath = ""
		case strings.HasPrefix(line, "rename from "):
			cur.OldPath = strings.TrimPrefix(line, "rename from ")
		case strings.HasPrefix(line, "rename to "):
			cur.NewPath = strings.TrimPrefix(line, "rename to ")
		case strings.HasPrefix(line, "Binary files ") || strings.HasPrefix(line, "GIT binary patch"):
			cur.Binary = true
		case strings.HasPrefix(line, "--- "):
			if p := diffPath(strings.TrimPrefix(line, "--- ")); p == "" {
				cur.OldPath = ""
			} else {
				cur.OldPath = p
			}
		case strings.HasPrefix(line, "+++ "):
			if p := diffPath(strings.TrimPrefix(line, "+++ ")); p == "" {
				cur.NewPath = ""
			} else {
				cur.NewPath = p
			}
		case strings.HasPrefix(line, "@@ "):
			os, oc, nc, err := parseHunkHeader(line)
			if err != nil {
				return nil, err
			}
			oldLine, oldLeft, newLeft = os, oc, nc
			if oc == 0 {
				// Pure insertion: "-k,0" means after line k.
				oldLine = os + 1
			}
		}
	}
	flush()
	return out, sc.Err()
}

// diffPath strips the a/ or b/ prefix and any trailing tab metadata;
// /dev/null becomes "".
func diffPath(s string) string {
	if i := strings.IndexByte(s, '\t'); i >= 0 {
		s = s[:i]
	}
	s = unquote(strings.TrimSpace(s))
	if s == "/dev/null" {
		return ""
	}
	if strings.HasPrefix(s, "a/") || strings.HasPrefix(s, "b/") {
		return s[2:]
	}
	return s
}

func splitGitHeader(s string) (string, string) {
	// "a/x b/x" (paths without spaces are by far the common case).
	if i := strings.Index(s, " b/"); i >= 0 {
		return diffPath(s[:i]), diffPath(s[i+1:])
	}
	f := strings.Fields(s)
	if len(f) == 2 {
		return diffPath(f[0]), diffPath(f[1])
	}
	return "", ""
}

func unquote(s string) string {
	if len(s) >= 2 && s[0] == '"' {
		if u, err := strconv.Unquote(s); err == nil {
			return u
		}
	}
	return s
}

// parseHunkHeader parses "@@ -a,b +c,d @@ ..." and returns a, b, d.
func parseHunkHeader(line string) (oldStart, oldCount, newCount int, err error) {
	f := strings.Fields(line)
	if len(f) < 3 || !strings.HasPrefix(f[1], "-") || !strings.HasPrefix(f[2], "+") {
		return 0, 0, 0, fmt.Errorf("bad hunk header %q", line)
	}
	oldStart, oldCount, err = parseRange(f[1][1:])
	if err != nil {
		return 0, 0, 0, fmt.Errorf("bad hunk header %q: %w", line, err)
	}
	_, newCount, err = parseRange(f[2][1:])
	if err != nil {
		return 0, 0, 0, fmt.Errorf("bad hunk header %q: %w", line, err)
	}
	return oldStart, oldCount, newCount, nil
}

func parseRange(s string) (start, count int, err error) {
	a, b, ok := strings.Cut(s, ",")
	start, err = strconv.Atoi(a)
	if err != nil {
		return 0, 0, err
	}
	count = 1
	if ok {
		count, err = strconv.Atoi(b)
	}
	return start, count, err
}
