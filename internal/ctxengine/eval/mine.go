package eval

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"path"
	"regexp"
	"sort"
	"strings"
)

// MineOptions controls MineGitTasks.
type MineOptions struct {
	Repo string // "owner/name" recorded on each task
	// Revs are passed to `git rev-list` (e.g. "--all").
	Revs []string
	// Main is the mainline ref; when two commits carry the same change
	// (a branch commit and its squash merge), the mainline one wins.
	Main string
	// MaxGoldFiles drops sweeping commits (initial imports, repo-wide
	// formatting) whose gold set is too broad to be a retrieval task.
	MaxGoldFiles int
	// PRRef is a fmt pattern for a pull request's head ref, e.g.
	// "refs/remotes/origin/pr/%d". When a mainline commit's subject ends
	// in "(#N)" and that PR's own commits qualify as tasks, the squash
	// commit is dropped in favour of the finer-grained originals.
	PRRef string
	// MaxAddedFiles drops scaffolding commits that mostly create new
	// code, which no retriever can find at the base commit.
	MaxAddedFiles int
}

// MineGitTasks turns a Go repository's history into retrieval tasks.
// A commit qualifies when it has exactly one parent, modifies at least
// one existing non-test .go file (only those are gold), touches at least one _test.go file,
// and has at most MaxGoldFiles gold files. Duplicate changes (same
// `git patch-id --stable`) are kept once.
//
// The problem statement is the commit message, sanitized against the
// most direct leak: the conventional-commit prefix ("fix(tui):"), PR
// numbers, trailers, and every literal mention of a file the commit
// touches (full path, base name, or a multi-segment directory such as
// "internal/plugins") are removed or replaced by "<file>". Function
// and type names are kept, as an issue reporter would name them.
func MineGitTasks(ctx context.Context, gitDir string, opt MineOptions) ([]Task, error) {
	if opt.MaxGoldFiles <= 0 {
		opt.MaxGoldFiles = 10
	}
	if opt.MaxAddedFiles <= 0 {
		opt.MaxAddedFiles = 10
	}
	revs := opt.Revs
	if len(revs) == 0 {
		revs = []string{"--all"}
	}
	out, err := gitOut(ctx, gitDir, append([]string{"rev-list", "--no-merges", "--parents"}, revs...)...)
	if err != nil {
		return nil, err
	}
	onMain := map[string]bool{}
	if opt.Main != "" {
		m, err := gitOut(ctx, gitDir, "rev-list", opt.Main)
		if err != nil {
			return nil, err
		}
		for _, s := range strings.Fields(string(m)) {
			onMain[s] = true
		}
	}
	type cand struct {
		task    Task
		patchID string
	}
	var cands []cand
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		f := strings.Fields(line)
		if len(f) != 2 {
			continue // root commit or merge
		}
		commit, parent := f[0], f[1]
		patch, err := gitOut(ctx, gitDir, "-c", "core.quotepath=false", "diff", "--no-color", "--no-ext-diff",
			"--no-renames", "--src-prefix=a/", "--dst-prefix=b/", parent, commit)
		if err != nil {
			return nil, err
		}
		diffs, err := ParseUnifiedDiff(string(patch))
		if err != nil {
			return nil, fmt.Errorf("%s: %w", commit, err)
		}
		gold, added, ok := qualify(diffs, opt.MaxGoldFiles)
		if !ok || len(added) > opt.MaxAddedFiles {
			continue
		}
		msg, err := gitOut(ctx, gitDir, "log", "-1", "--format=%B", commit)
		if err != nil {
			return nil, err
		}
		meta, err := gitOut(ctx, gitDir, "log", "-1", "--format=%cI", commit)
		if err != nil {
			return nil, err
		}
		var touched []string
		for _, d := range diffs {
			touched = append(touched, d.Path())
		}
		snap := &GitSnapshot{Dir: gitDir, Commit: parent}
		funcs := GoldFunctions(diffs, GoldOptions{}, snap.ReadFile)
		pid, err := patchID(ctx, gitDir, patch)
		if err != nil {
			return nil, err
		}
		cands = append(cands, cand{
			task: Task{
				ID:               "forge-" + commit[:12],
				Repo:             opt.Repo,
				BaseCommit:       parent,
				ProblemStatement: SanitizeCommitMessage(string(msg), touched),
				GoldPatch:        FilterPatch(string(patch), gold),
				GoldFiles:        gold,
				GoldFunctions:    funcs,
				AddedFiles:       added,
				Split:            SplitInternal,
				Source:           "forge-git",
				FixCommit:        commit,
				CreatedAt:        strings.TrimSpace(string(meta)),
			},
			patchID: pid,
		})
	}
	// Squash merges duplicate their PR's commits; keep the originals.
	if opt.PRRef != "" && opt.Main != "" {
		byCommit := map[string]bool{}
		for _, c := range cands {
			byCommit[c.task.FixCommit] = true
		}
		kept := cands[:0]
		for _, c := range cands {
			if onMain[c.task.FixCommit] && squashedPR(ctx, gitDir, c.task.FixCommit, opt, byCommit) {
				continue
			}
			kept = append(kept, c)
		}
		cands = kept
	}
	// A branch commit and its squash merge can differ slightly (the
	// squash folds in review fixes), so also treat an identical subject
	// line as the same change.
	for i := range cands {
		subj, _, _ := strings.Cut(cands[i].task.ProblemStatement, "\n")
		for j := 0; j < i; j++ {
			other, _, _ := strings.Cut(cands[j].task.ProblemStatement, "\n")
			if subj == other {
				cands[i].patchID = cands[j].patchID
			}
		}
	}
	// Dedupe by patch id, preferring mainline, then the earliest commit.
	sort.Slice(cands, func(i, j int) bool {
		a, b := cands[i], cands[j]
		if a.patchID != b.patchID {
			return a.patchID < b.patchID
		}
		if onMain[a.task.FixCommit] != onMain[b.task.FixCommit] {
			return onMain[a.task.FixCommit]
		}
		if a.task.CreatedAt != b.task.CreatedAt {
			return a.task.CreatedAt < b.task.CreatedAt
		}
		return a.task.FixCommit < b.task.FixCommit
	})
	var tasks []Task
	for i, c := range cands {
		if i > 0 && c.patchID == cands[i-1].patchID {
			continue
		}
		tasks = append(tasks, c.task)
	}
	sort.Slice(tasks, func(i, j int) bool { return tasks[i].ID < tasks[j].ID })
	return tasks, nil
}

var prSuffixRe = regexp.MustCompile(`\(#(\d+)\)\s*$`)

// squashedPR reports whether commit is a "(#N)" squash merge whose PR
// head contributes at least one off-mainline candidate.
func squashedPR(ctx context.Context, gitDir, commit string, opt MineOptions, cands map[string]bool) bool {
	subj, err := gitOut(ctx, gitDir, "log", "-1", "--format=%s", commit)
	if err != nil {
		return false
	}
	m := prSuffixRe.FindStringSubmatch(strings.TrimSpace(string(subj)))
	if m == nil {
		return false
	}
	ref := fmt.Sprintf(opt.PRRef, atoiOr0(m[1]))
	out, err := gitOut(ctx, gitDir, "rev-list", "--no-merges", opt.Main+".."+ref)
	if err != nil {
		return false
	}
	for _, c := range strings.Fields(string(out)) {
		if cands[c] {
			return true
		}
	}
	return false
}

func atoiOr0(s string) int {
	n := 0
	for _, r := range s {
		n = n*10 + int(r-'0')
	}
	return n
}

// FilterPatch keeps only the sections of a git patch whose base-side
// path is in keep. Mined tasks store the gold part of the change; the
// whole commit stays reproducible with `git diff base_commit fix_commit`.
func FilterPatch(patch string, keep []string) string {
	want := map[string]bool{}
	for _, k := range keep {
		want[k] = true
	}
	var b strings.Builder
	sections := strings.SplitAfter(patch, "\n")
	include := false
	for _, line := range sections {
		if strings.HasPrefix(line, "diff --git ") {
			a, _ := splitGitHeader(strings.TrimSuffix(strings.TrimPrefix(line, "diff --git "), "\n"))
			include = want[a]
		}
		if include {
			b.WriteString(line)
		}
	}
	return b.String()
}

func qualify(diffs []FileDiff, maxGold int) (gold, added []string, ok bool) {
	tests := false
	for _, d := range diffs {
		if strings.HasSuffix(d.Path(), "_test.go") {
			tests = true
		}
	}
	if !tests {
		return nil, nil, false
	}
	allGold, allAdded := GoldFiles(diffs, GoldOptions{})
	// Gold is Go source only: CHANGELOG, README, CI and manifest edits
	// ride along with fixes but are not the context a fix needs.
	for _, g := range allGold {
		if strings.HasSuffix(g, ".go") {
			gold = append(gold, g)
		}
	}
	for _, a := range allAdded {
		if strings.HasSuffix(a, ".go") {
			added = append(added, a)
		}
	}
	if len(gold) == 0 || len(gold) > maxGold {
		return nil, nil, false
	}
	return gold, added, true
}

func patchID(ctx context.Context, gitDir string, patch []byte) (string, error) {
	cmd := exec.CommandContext(ctx, "git", "-C", gitDir, "patch-id", "--stable")
	cmd.Stdin = bytes.NewReader(patch)
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git patch-id: %w", err)
	}
	f := strings.Fields(string(out))
	if len(f) == 0 {
		return "", fmt.Errorf("git patch-id: no output")
	}
	return f[0], nil
}

var (
	ccPrefixRe = regexp.MustCompile(`^[a-z]+(\([^)]*\))?!?:\s*`)
	prNumRe    = regexp.MustCompile(`\s*\(#\d+\)`)
	trailerRe  = regexp.MustCompile(`^[A-Z][A-Za-z-]+: .+`)
)

// SanitizeCommitMessage turns a commit message into a problem statement
// (see MineGitTasks for what is removed and why).
func SanitizeCommitMessage(msg string, touched []string) string {
	lines := strings.Split(strings.ReplaceAll(msg, "\r\n", "\n"), "\n")
	// Drop the trailer block (Co-authored-by:, Signed-off-by:, ...).
	end := len(lines)
	for end > 0 && strings.TrimSpace(lines[end-1]) == "" {
		end--
	}
	for end > 0 && trailerRe.MatchString(strings.TrimSpace(lines[end-1])) {
		end--
	}
	lines = lines[:end]
	if len(lines) > 0 {
		lines[0] = ccPrefixRe.ReplaceAllString(lines[0], "")
	}
	s := prNumRe.ReplaceAllString(strings.Join(lines, "\n"), "")
	// Longest names first so a path is replaced before its base name.
	names := map[string]bool{}
	for _, p := range touched {
		names[p] = true
		names[path.Base(p)] = true
		// Directories that name a package ("internal/plugins") point at
		// the gold file almost as directly as its path. Single-segment
		// directories ("internal", "cmd") are too generic to mask.
		for d := path.Dir(p); strings.Contains(d, "/"); d = path.Dir(d) {
			names[d+"/"] = true
			names[d] = true
		}
	}
	var ns []string
	for n := range names {
		ns = append(ns, n)
	}
	sort.Slice(ns, func(i, j int) bool {
		if len(ns[i]) != len(ns[j]) {
			return len(ns[i]) > len(ns[j])
		}
		return ns[i] < ns[j]
	})
	for _, n := range ns {
		s = replaceWord(s, n, "<file>")
	}
	return strings.TrimSpace(s)
}

// replaceWord replaces occurrences of word not embedded in a longer
// path or identifier.
func replaceWord(s, word, repl string) string {
	var b strings.Builder
	for {
		j := strings.Index(s, word)
		if j < 0 {
			b.WriteString(s)
			return b.String()
		}
		before := j == 0 || !isPathByte(s[j-1])
		after := j+len(word) == len(s) || !isPathByte(s[j+len(word)])
		if before && after {
			b.WriteString(s[:j] + repl)
		} else {
			b.WriteString(s[:j+len(word)])
		}
		s = s[j+len(word):]
	}
}

func isPathByte(c byte) bool {
	return c == '_' || c == '/' || c == '-' || (c >= '0' && c <= '9') || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}
