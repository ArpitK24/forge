package eval

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"github.com/ArpitK24/forge/internal/ctxengine/retrieve"
)

// Defaults shared by the CLI and the docs.
const (
	DefaultBudget    = 8192
	DefaultResamples = 2000
	DefaultSeed      = 20261108
	DefaultDevSize   = 100
)

const usage = `forge eval: context-engine evaluation harness (hidden command)

Usage:
  forge eval retrieval [flags]       score baselines on a dataset, write JSON
  forge eval fetch-swebench [flags]  download SWE-bench Lite/Verified into the cache
  forge eval split [flags]           regenerate the Verified dev split id list
  forge eval mine [flags]            mine internal tasks from a Go repo's history

Run "forge eval <command> -h" for flags. See eval/README.md.
`

// Main runs `forge eval` and returns the process exit code.
func Main(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" || args[0] == "help" {
		fmt.Fprint(stderr, usage)
		if len(args) == 0 {
			return 2
		}
		return 0
	}
	var err error
	switch args[0] {
	case "retrieval":
		err = cmdRetrieval(ctx, args[1:], stdout, stderr)
	case "fetch-swebench":
		err = cmdFetch(ctx, args[1:], stderr)
	case "split":
		err = cmdSplit(args[1:], stderr)
	case "mine":
		err = cmdMine(ctx, args[1:], stderr)
	default:
		fmt.Fprintf(stderr, "forge eval: unknown command %q\n\n%s", args[0], usage)
		return 2
	}
	if errors.Is(err, flag.ErrHelp) {
		return 0
	}
	if err != nil {
		var ue usageError
		if errors.As(err, &ue) {
			fmt.Fprintln(stderr, "forge eval:", err)
			return 2
		}
		fmt.Fprintln(stderr, "forge eval:", err)
		return 1
	}
	return 0
}

type usageError struct{ msg string }

func (e usageError) Error() string { return e.msg }

type repoFlag map[string]string

func (r repoFlag) String() string {
	var parts []string
	for k, v := range r {
		parts = append(parts, k+"="+v)
	}
	sort.Strings(parts)
	return strings.Join(parts, ",")
}

func (r repoFlag) Set(s string) error {
	k, v, ok := strings.Cut(s, "=")
	if !ok || k == "" || v == "" {
		return fmt.Errorf("want owner/name=path, got %q", s)
	}
	r[k] = v
	return nil
}

func newFlagSet(name string, stderr io.Writer) *flag.FlagSet {
	fs := flag.NewFlagSet("forge eval "+name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	return fs
}

func cmdRetrieval(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := newFlagSet("retrieval", stderr)
	dataset := fs.String("dataset", "internal", "internal | lite | verified | verified-dev")
	tasksPath := fs.String("tasks", "", "read tasks from this JSONL instead of the dataset's default")
	baselines := fs.String("baseline", "b0,b1,b2", "comma-separated baselines: b0, b1 (= b1-1k,b1-4k), b1-1k, b1-4k, b2")
	budget := fs.Int("budget", DefaultBudget, "pack token budget")
	out := fs.String("out", "", "write the JSON report here (default stdout)")
	evalDir := fs.String("eval-dir", "eval", "directory holding tasks/ and splits/")
	cacheDir := fs.String("cache-dir", DefaultCacheDir(), "cache for datasets and bare repo clones")
	noFetch := fs.Bool("no-fetch", false, "never clone or fetch repositories")
	limit := fs.Int("limit", 0, "only the first N tasks by id (0 = all)")
	jobs := fs.Int("jobs", min(runtime.NumCPU(), 4), "tasks evaluated in parallel")
	resamples := fs.Int("bootstrap", DefaultResamples, "bootstrap resamples")
	seed := fs.Uint64("seed", DefaultSeed, "bootstrap seed")
	includeTests := fs.Bool("include-tests", false, "count test files as gold (internal tasks only)")
	strict := fs.Bool("strict", false, "fail if any task is skipped")
	quiet := fs.Bool("quiet", false, "no per-task progress on stderr")
	repos := repoFlag{}
	fs.Var(repos, "repo", "use a local clone: owner/name=path (repeatable)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	names, err := retrieve.ExpandNames(*baselines)
	if err != nil {
		return usageError{err.Error()}
	}
	tasks, err := loadDataset(*dataset, *tasksPath, *evalDir, *cacheDir)
	if err != nil {
		return err
	}
	if *includeTests {
		for i := range tasks {
			if tasks[i].Source == "forge-git" {
				tasks[i].GoldFiles, tasks[i].GoldFunctions = nil, nil
			}
		}
	}
	sort.Slice(tasks, func(i, j int) bool { return tasks[i].ID < tasks[j].ID })
	if *limit > 0 && *limit < len(tasks) {
		tasks = tasks[:*limit]
	}
	rc := &RepoCache{Dir: *cacheDir, Local: map[string]string{}, NoFetch: *noFetch}
	for k, v := range repos {
		abs, err := filepath.Abs(v)
		if err != nil {
			return err
		}
		rc.Local[k] = abs
	}
	fmt.Fprintf(stderr, "forge eval: %d tasks, baselines %s, budget %d\n", len(tasks), strings.Join(names, ","), *budget)
	rep, err := Run(ctx, tasks, rc, RunConfig{
		Dataset: *dataset, Baselines: names, Budget: *budget,
		Resamples: *resamples, Seed: *seed, Jobs: *jobs,
		Gold:     GoldOptions{IncludeTests: *includeTests},
		Progress: progressWriter(*quiet, stderr),
	})
	if err != nil {
		return err
	}
	b, err := MarshalReport(rep)
	if err != nil {
		return err
	}
	if *out == "" {
		_, err = stdout.Write(b)
	} else {
		err = os.WriteFile(*out, b, 0o644)
	}
	if err != nil {
		return err
	}
	fmt.Fprint(stderr, SummaryTable(rep))
	if len(rep.Skipped) > 0 {
		for _, s := range rep.Skipped {
			fmt.Fprintf(stderr, "skipped %s: %s\n", s.ID, s.Reason)
		}
		if *strict {
			return fmt.Errorf("%d of %d tasks skipped (--strict)", len(rep.Skipped), rep.Tasks)
		}
	}
	return nil
}

func loadDataset(name, tasksPath, evalDir, cacheDir string) ([]Task, error) {
	if tasksPath != "" {
		return ReadTasks(tasksPath)
	}
	switch name {
	case "internal":
		return ReadTasks(filepath.Join(evalDir, "tasks", "internal.jsonl"))
	case "lite", "verified", "verified-dev":
		d := SWEBenchVerified
		if name == "lite" {
			d = SWEBenchLite
		}
		dev, err := ReadIDList(filepath.Join(evalDir, "splits", "verified-dev.txt"))
		if err != nil {
			return nil, err
		}
		tasks, err := LoadSWEBench(d.CachePath(cacheDir), d, dev)
		if err != nil {
			return nil, err
		}
		if name != "verified-dev" {
			return tasks, nil
		}
		var out []Task
		for _, t := range tasks {
			if t.Split == SplitDev {
				out = append(out, t)
			}
		}
		if len(out) != len(dev) {
			return nil, fmt.Errorf("dev split lists %d ids but %d were found in the cached dataset", len(dev), len(out))
		}
		return out, nil
	}
	return nil, usageError{fmt.Sprintf("unknown dataset %q (want internal, lite, verified or verified-dev)", name)}
}

// SummaryTable renders the aggregate metrics as a Markdown table.
func SummaryTable(r *Report) string {
	var b strings.Builder
	fmt.Fprintf(&b, "\n%s: %d/%d tasks scored, budget %d tokens (%s)\n\n", r.Dataset, r.Scored, r.Tasks, r.Budget, r.Tokenizer)
	b.WriteString("| baseline | file R@1 | R@3 | R@5 | R@10 | func R@5 | func R@10 | MRR | gold-in-pack | pack tokens |\n")
	b.WriteString("|---|---|---|---|---|---|---|---|---|---|\n")
	for _, br := range r.Baselines {
		a := br.Aggregate
		fmt.Fprintf(&b, "| %s | %.3f | %.3f | %.3f | %.3f | %.3f | %.3f | %.3f | %.3f | %.0f |\n", br.Name,
			a[MFileR1].Mean, a[MFileR3].Mean, a[MFileR5].Mean, a[MFileR10].Mean,
			a[MFuncR5].Mean, a[MFuncR10].Mean, a[MMRR].Mean, a[MGoldInPack].Mean, a[MPackTokens].Mean)
	}
	return b.String()
}

func cmdFetch(ctx context.Context, args []string, stderr io.Writer) error {
	fs := newFlagSet("fetch-swebench", stderr)
	which := fs.String("dataset", "all", "lite | verified | all")
	cacheDir := fs.String("cache-dir", DefaultCacheDir(), "cache directory")
	if err := fs.Parse(args); err != nil {
		return err
	}
	var ds []SWEBenchDataset
	switch *which {
	case "lite":
		ds = []SWEBenchDataset{SWEBenchLite}
	case "verified":
		ds = []SWEBenchDataset{SWEBenchVerified}
	case "all":
		ds = []SWEBenchDataset{SWEBenchLite, SWEBenchVerified}
	default:
		return usageError{fmt.Sprintf("unknown dataset %q", *which)}
	}
	for _, d := range ds {
		p, err := FetchSWEBench(ctx, d, *cacheDir, nil, stderr)
		if err != nil {
			return err
		}
		fmt.Fprintf(stderr, "wrote %s\n", p)
	}
	return nil
}

func cmdSplit(args []string, stderr io.Writer) error {
	fs := newFlagSet("split", stderr)
	cacheDir := fs.String("cache-dir", DefaultCacheDir(), "cache directory")
	n := fs.Int("n", DefaultDevSize, "dev split size")
	seed := fs.Uint64("seed", DefaultSeed, "split seed")
	out := fs.String("out", filepath.Join("eval", "splits", "verified-dev.txt"), "output id list")
	if err := fs.Parse(args); err != nil {
		return err
	}
	tasks, err := LoadSWEBench(SWEBenchVerified.CachePath(*cacheDir), SWEBenchVerified, nil)
	if err != nil {
		return err
	}
	ids := DevSplit(tasks, *n, *seed)
	var b strings.Builder
	fmt.Fprintf(&b, "# SWE-bench Verified dev split: %d of %d tasks, stratified by repo, seed %d.\n", len(ids), len(tasks), *seed)
	b.WriteString("# Regenerate: forge eval split. Everything else in Verified is held out.\n")
	for _, id := range ids {
		b.WriteString(id + "\n")
	}
	if err := os.MkdirAll(filepath.Dir(*out), 0o755); err != nil {
		return err
	}
	return os.WriteFile(*out, []byte(b.String()), 0o644)
}

func cmdMine(ctx context.Context, args []string, stderr io.Writer) error {
	fs := newFlagSet("mine", stderr)
	repoDir := fs.String("repo-dir", ".", "git repository to mine")
	repo := fs.String("repo", "ArpitK24/forge", "owner/name recorded on the tasks")
	revs := fs.String("revs", "--all", "space-separated rev-list arguments")
	mainRef := fs.String("main", "origin/main", "mainline ref preferred when deduplicating")
	maxGold := fs.Int("max-gold-files", 10, "drop commits with more gold files than this")
	prRef := fs.String("pr-ref", "refs/remotes/origin/pr/%d", "PR head ref pattern (fetch +refs/pull/*/head:refs/remotes/origin/pr/*); \"\" keeps squash merges")
	out := fs.String("out", filepath.Join("eval", "tasks", "internal.jsonl"), "output JSONL")
	if err := fs.Parse(args); err != nil {
		return err
	}
	tasks, err := MineGitTasks(ctx, *repoDir, MineOptions{
		Repo: *repo, Revs: strings.Fields(*revs), Main: *mainRef, MaxGoldFiles: *maxGold, PRRef: *prRef,
	})
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(*out), 0o755); err != nil {
		return err
	}
	if err := WriteTasks(*out, tasks); err != nil {
		return err
	}
	fmt.Fprintf(stderr, "wrote %d tasks to %s\n", len(tasks), *out)
	return nil
}

func progressWriter(quiet bool, w io.Writer) io.Writer {
	if quiet {
		return nil
	}
	return w
}
