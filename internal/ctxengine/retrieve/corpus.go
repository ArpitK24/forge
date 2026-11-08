package retrieve

import (
	"bytes"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/ArpitK24/forge/internal/ctxengine/symbols"
)

// FileInfo describes one file in a snapshot.
type FileInfo struct {
	Path string // slash-separated, relative to the repo root
	Size int64
}

// Snapshot is a read-only view of a repository at one commit.
type Snapshot interface {
	// Files lists regular files (no symlinks or submodules).
	Files() ([]FileInfo, error)
	// ReadFiles returns the contents of the given paths.
	ReadFiles(paths []string) (map[string][]byte, error)
}

// MaxFileBytes is the largest file the corpus indexes.
const MaxFileBytes = 1 << 20

// indexExt is the set of extensions the corpus indexes: source code,
// build files and prose. Anything else (images, data, lockfiles) is
// invisible to every baseline alike.
var indexExt = map[string]bool{}

func init() {
	for _, e := range strings.Fields(`.go .py .pyi .pyx .js .jsx .ts .tsx .mjs .cjs
		.java .kt .scala .rb .rs .c .h .cc .cpp .hpp .cs .swift .php .lua .sh .bash
		.sql .md .rst .txt .toml .yaml .yml .cfg .ini .json .html .css .tex .mod`) {
		indexExt[e] = true
	}
}

// Indexable reports whether the corpus would index a path of this size.
func Indexable(p string, size int64) bool {
	if size > MaxFileBytes {
		return false
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == "node_modules" || seg == "vendor" || seg == ".git" {
			return false
		}
	}
	return indexExt[strings.ToLower(path.Ext(p))]
}

// Corpus is the indexable part of a snapshot, read once and shared by
// every retriever run on the same task.
type Corpus struct {
	files   []string
	content map[string]string

	mu   sync.Mutex
	syms map[string][]symbols.Symbol
}

// NewCorpus reads every indexable, non-binary file of snap.
func NewCorpus(snap Snapshot) (*Corpus, error) {
	infos, err := snap.Files()
	if err != nil {
		return nil, err
	}
	var paths []string
	for _, fi := range infos {
		if Indexable(fi.Path, fi.Size) {
			paths = append(paths, fi.Path)
		}
	}
	sort.Strings(paths)
	data, err := snap.ReadFiles(paths)
	if err != nil {
		return nil, err
	}
	c := &Corpus{content: map[string]string{}, syms: map[string][]symbols.Symbol{}}
	for _, p := range paths {
		b, ok := data[p]
		if !ok {
			return nil, fmt.Errorf("snapshot did not return %s", p)
		}
		if bytes.IndexByte(b[:min(len(b), 8000)], 0) >= 0 {
			continue // binary
		}
		c.files = append(c.files, p)
		c.content[p] = string(b)
	}
	return c, nil
}

// Files returns the indexed paths, sorted.
func (c *Corpus) Files() []string { return c.files }

// Content returns a file's text ("" when not indexed).
func (c *Corpus) Content(p string) string { return c.content[p] }

// Has reports whether p is indexed.
func (c *Corpus) Has(p string) bool { _, ok := c.content[p]; return ok }

// Symbols returns the symbols defined in p, memoized.
func (c *Corpus) Symbols(p string) []symbols.Symbol {
	c.mu.Lock()
	defer c.mu.Unlock()
	if s, ok := c.syms[p]; ok {
		return s
	}
	s := symbols.Extract(p, []byte(c.content[p]))
	c.syms[p] = s
	return s
}

// MapSnapshot is an in-memory Snapshot, used by tests.
type MapSnapshot map[string]string

// Files implements Snapshot.
func (m MapSnapshot) Files() ([]FileInfo, error) {
	out := make([]FileInfo, 0, len(m))
	for p, s := range m {
		out = append(out, FileInfo{Path: p, Size: int64(len(s))})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}

// ReadFiles implements Snapshot.
func (m MapSnapshot) ReadFiles(paths []string) (map[string][]byte, error) {
	out := make(map[string][]byte, len(paths))
	for _, p := range paths {
		s, ok := m[p]
		if !ok {
			return nil, fmt.Errorf("%s: not in snapshot", p)
		}
		out[p] = []byte(s)
	}
	return out, nil
}

// DirSnapshot is a Snapshot of a working tree on disk.
type DirSnapshot string

// Files implements Snapshot.
func (d DirSnapshot) Files() ([]FileInfo, error) {
	var out []FileInfo
	root := string(d)
	err := filepath.WalkDir(root, func(p string, e os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if e.IsDir() {
			if e.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		if !e.Type().IsRegular() {
			return nil
		}
		info, err := e.Info()
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		out = append(out, FileInfo{Path: filepath.ToSlash(rel), Size: info.Size()})
		return nil
	})
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, err
}

// ReadFiles implements Snapshot.
func (d DirSnapshot) ReadFiles(paths []string) (map[string][]byte, error) {
	out := make(map[string][]byte, len(paths))
	for _, p := range paths {
		b, err := os.ReadFile(filepath.Join(string(d), filepath.FromSlash(p)))
		if err != nil {
			return nil, err
		}
		out[p] = b
	}
	return out, nil
}
