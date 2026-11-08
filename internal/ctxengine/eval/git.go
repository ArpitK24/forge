package eval

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/ArpitK24/forge/internal/ctxengine/retrieve"
)

// RepoCache resolves (repo, commit) to a snapshot read straight from a
// git object database. Remote repositories are kept as bare clones
// under Dir/repos/<owner>__<name>.git and reused across runs; Local
// maps a repo to an existing clone (e.g. "ArpitK24/forge" -> ".").
//
// Snapshots read blobs with `git ls-tree` and `git cat-file --batch`
// instead of checking out a worktree per task: it is the same tree as a
// checkout at base_commit, costs no disk per task, and cannot be dirtied.
type RepoCache struct {
	Dir     string
	Local   map[string]string
	NoFetch bool // never clone or fetch; fail if the commit is missing
	// RemoteURL overrides the clone URL (tests); default is GitHub.
	RemoteURL func(repo string) string

	mu    sync.Mutex
	locks map[string]*sync.Mutex
}

// DefaultCacheDir is <user cache dir>/forge/eval.
func DefaultCacheDir() string {
	if d, err := os.UserCacheDir(); err == nil {
		return filepath.Join(d, "forge", "eval")
	}
	return filepath.Join(os.TempDir(), "forge-eval-cache")
}

func (rc *RepoCache) repoLock(repo string) *sync.Mutex {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	if rc.locks == nil {
		rc.locks = map[string]*sync.Mutex{}
	}
	l, ok := rc.locks[repo]
	if !ok {
		l = &sync.Mutex{}
		rc.locks[repo] = l
	}
	return l
}

// GitDir returns the git directory for repo, cloning it if needed.
func (rc *RepoCache) GitDir(ctx context.Context, repo string) (string, error) {
	if p, ok := rc.Local[repo]; ok {
		return p, nil
	}
	if strings.Count(repo, "/") != 1 || strings.Contains(repo, "..") {
		return "", fmt.Errorf("bad repo name %q (want owner/name)", repo)
	}
	dir := filepath.Join(rc.Dir, "repos", strings.ReplaceAll(repo, "/", "__")+".git")
	if _, err := os.Stat(filepath.Join(dir, "HEAD")); err == nil {
		return dir, nil
	}
	if rc.NoFetch {
		return "", fmt.Errorf("%s not cached under %s and fetching is disabled", repo, rc.Dir)
	}
	url := "https://github.com/" + repo + ".git"
	if rc.RemoteURL != nil {
		url = rc.RemoteURL(repo)
	}
	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		return "", err
	}
	tmp := dir + ".partial"
	_ = os.RemoveAll(tmp)
	if _, err := gitOut(ctx, "", "clone", "--quiet", "--bare", url, tmp); err != nil {
		return "", err
	}
	if err := os.Rename(tmp, dir); err != nil {
		return "", err
	}
	return dir, nil
}

// Snapshot implements SnapshotSource.
func (rc *RepoCache) Snapshot(ctx context.Context, repo, commit string) (retrieve.Snapshot, error) {
	l := rc.repoLock(repo)
	l.Lock()
	defer l.Unlock()
	dir, err := rc.GitDir(ctx, repo)
	if err != nil {
		return nil, err
	}
	if !hasCommit(ctx, dir, commit) {
		if rc.NoFetch {
			return nil, fmt.Errorf("commit %s not in %s (fetching disabled)", commit, dir)
		}
		// GitHub serves any reachable SHA; pull request heads cover
		// commits from branches that were squash-merged and deleted.
		_, _ = gitOut(ctx, dir, "fetch", "--quiet", "origin", commit)
		if !hasCommit(ctx, dir, commit) {
			_, _ = gitOut(ctx, dir, "fetch", "--quiet", "origin", "+refs/pull/*/head:refs/remotes/origin/pr/*")
		}
		if !hasCommit(ctx, dir, commit) {
			return nil, fmt.Errorf("commit %s not found in %s", commit, repo)
		}
	}
	return &GitSnapshot{Dir: dir, Commit: commit}, nil
}

func hasCommit(ctx context.Context, dir, commit string) bool {
	_, err := gitOut(ctx, dir, "cat-file", "-e", commit+"^{commit}")
	return err == nil
}

// GitSnapshot is a retrieve.Snapshot of one commit in a git directory.
type GitSnapshot struct {
	Dir    string
	Commit string

	mu    sync.Mutex
	blobs map[string]string // path -> blob sha
}

// Files implements retrieve.Snapshot.
func (g *GitSnapshot) Files() ([]retrieve.FileInfo, error) {
	var out []byte
	err := retryRead(func() error {
		var e error
		out, e = gitOut(context.Background(), g.Dir, "ls-tree", "-r", "-z", "--long", "--full-tree", g.Commit)
		return e
	})
	if err != nil {
		return nil, err
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.blobs = map[string]string{}
	var files []retrieve.FileInfo
	for _, rec := range bytes.Split(out, []byte{0}) {
		if len(rec) == 0 {
			continue
		}
		meta, p, ok := bytes.Cut(rec, []byte{'\t'})
		if !ok {
			continue
		}
		f := strings.Fields(string(meta)) // mode type sha size
		if len(f) != 4 || f[1] != "blob" || (f[0] != "100644" && f[0] != "100755") {
			continue // symlinks, submodules
		}
		size, _ := strconv.ParseInt(f[3], 10, 64)
		g.blobs[string(p)] = f[2]
		files = append(files, retrieve.FileInfo{Path: string(p), Size: size})
	}
	return files, nil
}

// ReadFiles implements retrieve.Snapshot.
func (g *GitSnapshot) ReadFiles(paths []string) (map[string][]byte, error) {
	g.mu.Lock()
	if g.blobs == nil {
		g.mu.Unlock()
		if _, err := g.Files(); err != nil {
			return nil, err
		}
		g.mu.Lock()
	}
	shas := make([]string, len(paths))
	for i, p := range paths {
		sha, ok := g.blobs[p]
		if !ok {
			g.mu.Unlock()
			return nil, fmt.Errorf("%s: not in %s", p, g.Commit)
		}
		shas[i] = sha
	}
	g.mu.Unlock()
	var out map[string][]byte
	err := retryRead(func() error {
		var e error
		out, e = catBlobs(g.Dir, paths, shas)
		return e
	})
	return out, err
}

// retryRead retries a read-only git call a few times. Object reads are
// idempotent, and git can die on a transient mmap/IO fault when the
// cache sits on a network filesystem.
func retryRead(f func() error) error {
	var err error
	for attempt := 0; attempt < 3; attempt++ {
		if err = f(); err == nil {
			return nil
		}
	}
	return err
}

// ReadFile returns one file's content and whether it exists.
func (g *GitSnapshot) ReadFile(p string) ([]byte, bool) {
	m, err := g.ReadFiles([]string{p})
	if err != nil {
		return nil, false
	}
	return m[p], true
}

func catBlobs(dir string, paths, shas []string) (map[string][]byte, error) {
	out := make(map[string][]byte, len(paths))
	if len(paths) == 0 {
		return out, nil
	}
	cmd := exec.Command("git", "-C", dir, "cat-file", "--batch")
	var in bytes.Buffer
	for _, s := range shas {
		in.WriteString(s + "\n")
	}
	cmd.Stdin = &in
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	r := bufio.NewReaderSize(stdout, 1<<20)
	var readErr error
	for i := range shas {
		hdr, err := r.ReadString('\n')
		if err != nil {
			readErr = fmt.Errorf("cat-file header: %w", err)
			break
		}
		f := strings.Fields(hdr) // sha type size
		if len(f) != 3 {
			readErr = fmt.Errorf("cat-file: %q", strings.TrimSpace(hdr))
			break
		}
		n, err := strconv.Atoi(f[2])
		if err != nil {
			readErr = err
			break
		}
		buf := make([]byte, n+1) // content + trailing LF
		if _, err := io.ReadFull(r, buf); err != nil {
			readErr = err
			break
		}
		out[paths[i]] = buf[:n]
	}
	_, _ = io.Copy(io.Discard, r)
	if err := cmd.Wait(); err != nil && readErr == nil {
		readErr = fmt.Errorf("git cat-file: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return out, readErr
}

func gitOut(ctx context.Context, dir string, args ...string) ([]byte, error) {
	if dir != "" {
		args = append([]string{"-C", dir}, args...)
	}
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) || stderr.Len() > 0 {
			return out, fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
		}
		return out, fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return out, nil
}
