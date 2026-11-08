package index

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// FileStat is what Sync compares against the stored row before reading
// a file.
type FileStat struct {
	Path    string // slash-separated, relative to the source root
	Size    int64
	MtimeNS int64
}

// Source is a tree of files the index can mirror.
type Source interface {
	// List returns every file the index should hold, sorted by path.
	List(ctx context.Context) ([]FileStat, error)
	// Stat returns one file's stat; ok is false when the file is gone
	// or no longer indexable.
	Stat(path string) (st FileStat, ok bool, err error)
	// Read returns a file's content.
	Read(path string) ([]byte, error)
}

// Filter decides whether a path of the given size is indexed.
type Filter func(path string, size int64) bool

// DirSource is a working tree on disk. Files come from
// `git ls-files --cached --others --exclude-standard` when the root is
// inside a git work tree (so .gitignore is honoured exactly as git
// does); otherwise from a walk that skips dot-directories, vendor and
// node_modules. Paths matched by a .forgeignore at the root (one
// prefix or path.Match glob per line) are skipped in both modes.
type DirSource struct {
	Root   string
	Filter Filter

	ignore []string
}

// NewDirSource returns a DirSource for root.
func NewDirSource(root string, filter Filter) *DirSource {
	d := &DirSource{Root: root, Filter: filter}
	if b, err := os.ReadFile(filepath.Join(root, ".forgeignore")); err == nil {
		for _, l := range strings.Split(string(b), "\n") {
			l = strings.TrimSpace(l)
			if l != "" && !strings.HasPrefix(l, "#") {
				d.ignore = append(d.ignore, strings.TrimPrefix(l, "/"))
			}
		}
	}
	return d
}

func (d *DirSource) ignored(p string) bool {
	if strings.HasPrefix(p, ".forge/") || strings.HasPrefix(p, ".git/") {
		return true
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == "vendor" || seg == "node_modules" {
			return true
		}
	}
	for _, pat := range d.ignore {
		if strings.HasSuffix(pat, "/") && strings.HasPrefix(p, pat) {
			return true
		}
		if p == pat || strings.HasPrefix(p, pat+"/") {
			return true
		}
		if ok, _ := path.Match(pat, p); ok {
			return true
		}
		if ok, _ := path.Match(pat, path.Base(p)); ok {
			return true
		}
	}
	return false
}

// List implements Source.
func (d *DirSource) List(ctx context.Context) ([]FileStat, error) {
	paths, err := d.gitFiles(ctx)
	if err != nil {
		paths, err = d.walk()
		if err != nil {
			return nil, err
		}
	}
	out := make([]FileStat, 0, len(paths))
	for _, p := range paths {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		st, ok, err := d.Stat(p)
		if err != nil {
			return nil, err
		}
		if ok {
			out = append(out, st)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}

func (d *DirSource) gitFiles(ctx context.Context) ([]string, error) {
	if _, err := os.Stat(filepath.Join(d.Root, ".git")); err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, "git", "-C", d.Root, "ls-files", "-z",
		"--cached", "--others", "--exclude-standard")
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	var paths []string
	seen := map[string]bool{}
	for _, p := range bytes.Split(out, []byte{0}) {
		s := string(p)
		if s != "" && !seen[s] {
			seen[s] = true
			paths = append(paths, s)
		}
	}
	return paths, nil
}

func (d *DirSource) walk() ([]string, error) {
	var paths []string
	err := filepath.WalkDir(d.Root, func(p string, e fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if e.IsDir() {
			if p != d.Root && (strings.HasPrefix(e.Name(), ".") || e.Name() == "vendor" || e.Name() == "node_modules") {
				return filepath.SkipDir
			}
			return nil
		}
		rel, err := filepath.Rel(d.Root, p)
		if err != nil {
			return err
		}
		paths = append(paths, filepath.ToSlash(rel))
		return nil
	})
	return paths, err
}

// Stat implements Source.
func (d *DirSource) Stat(p string) (FileStat, bool, error) {
	if d.ignored(p) {
		return FileStat{}, false, nil
	}
	fi, err := os.Lstat(filepath.Join(d.Root, filepath.FromSlash(p)))
	if errors.Is(err, fs.ErrNotExist) {
		return FileStat{}, false, nil
	}
	if err != nil {
		return FileStat{}, false, err
	}
	if !fi.Mode().IsRegular() {
		return FileStat{}, false, nil
	}
	if d.Filter != nil && !d.Filter(p, fi.Size()) {
		return FileStat{}, false, nil
	}
	return FileStat{Path: p, Size: fi.Size(), MtimeNS: fi.ModTime().UnixNano()}, true, nil
}

// Read implements Source.
func (d *DirSource) Read(p string) ([]byte, error) {
	return os.ReadFile(filepath.Join(d.Root, filepath.FromSlash(p)))
}

// MapSource is an in-memory Source. Every file has mtime 0, so Sync
// falls back to comparing sizes and then content hashes.
type MapSource map[string][]byte

// List implements Source.
func (m MapSource) List(context.Context) ([]FileStat, error) {
	out := make([]FileStat, 0, len(m))
	for p, b := range m {
		out = append(out, FileStat{Path: p, Size: int64(len(b))})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}

// Stat implements Source.
func (m MapSource) Stat(p string) (FileStat, bool, error) {
	b, ok := m[p]
	if !ok {
		return FileStat{}, false, nil
	}
	return FileStat{Path: p, Size: int64(len(b))}, true, nil
}

// Read implements Source.
func (m MapSource) Read(p string) ([]byte, error) {
	b, ok := m[p]
	if !ok {
		return nil, fs.ErrNotExist
	}
	return b, nil
}
