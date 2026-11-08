package eval

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"sort"
	"sync"

	"github.com/ArpitK24/forge/internal/ctxengine/retrieve"
	"github.com/ArpitK24/forge/internal/ctxengine/symbols"
	"github.com/ArpitK24/forge/internal/ctxengine/tokens"
)

// SchemaVersion is bumped whenever the report layout or a metric
// definition changes, so stale baselines are never compared silently.
const SchemaVersion = 1

// SnapshotSource resolves a task's repository at its base commit.
type SnapshotSource interface {
	Snapshot(ctx context.Context, repo, commit string) (retrieve.Snapshot, error)
}

// RunConfig configures a retrieval run.
type RunConfig struct {
	Dataset   string
	Baselines []string // canonical names, see retrieve.Names
	Budget    int
	Resamples int
	Seed      uint64
	Gold      GoldOptions
	Jobs      int
	// Progress, when set, gets one line per finished task (order
	// follows scheduling, so it is never part of the report).
	Progress io.Writer
}

// Report is the JSON written by `forge eval retrieval`. It contains no
// timestamps, hostnames or durations, so equal inputs give equal bytes.
type Report struct {
	SchemaVersion int              `json:"schema_version"`
	Dataset       string           `json:"dataset"`
	Budget        int              `json:"budget"`
	Tokenizer     string           `json:"tokenizer"`
	GoldTests     bool             `json:"gold_includes_tests"`
	Bootstrap     BootstrapInfo    `json:"bootstrap"`
	Tasks         int              `json:"tasks"`
	Scored        int              `json:"scored"`
	Skipped       []Skip           `json:"skipped"`
	Baselines     []BaselineReport `json:"baselines"`
	Paired        []Paired         `json:"paired"`
}

// BootstrapInfo records how the confidence intervals were computed.
type BootstrapInfo struct {
	Resamples int     `json:"resamples"`
	Seed      uint64  `json:"seed"`
	Level     float64 `json:"level"`
}

// Skip is a task the run could not score.
type Skip struct {
	ID     string `json:"id"`
	Reason string `json:"reason"`
}

// BaselineReport is one baseline's aggregate and per-task results.
type BaselineReport struct {
	Name      string          `json:"name"`
	Aggregate map[string]Stat `json:"aggregate"`
	Tasks     []TaskResult    `json:"tasks"`
}

type taskOutcome struct {
	task    Task
	results []TaskResult
	err     error
}

// Run evaluates every baseline on every task. Tasks are processed by
// cfg.Jobs workers but collected by index, so the report does not
// depend on scheduling.
func Run(ctx context.Context, tasks []Task, src SnapshotSource, cfg RunConfig) (*Report, error) {
	if cfg.Budget <= 0 {
		return nil, fmt.Errorf("budget must be positive")
	}
	var rets []retrieve.Retriever
	for _, n := range cfg.Baselines {
		r, err := retrieve.New(n)
		if err != nil {
			return nil, err
		}
		rets = append(rets, r)
	}
	sorted := append([]Task(nil), tasks...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].ID < sorted[j].ID })

	outcomes := make([]taskOutcome, len(sorted))
	jobs := max(cfg.Jobs, 1)
	sem := make(chan struct{}, jobs)
	var wg sync.WaitGroup
	var progressMu sync.Mutex
	done := 0
	for i := range sorted {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int) {
			defer wg.Done()
			defer func() { <-sem }()
			outcomes[i] = runTask(ctx, sorted[i], src, rets, cfg)
			if cfg.Progress != nil {
				status := "ok"
				if outcomes[i].err != nil {
					status = "skipped"
				}
				progressMu.Lock()
				done++
				fmt.Fprintf(cfg.Progress, "[%d/%d] %s %s\n", done, len(sorted), sorted[i].ID, status)
				progressMu.Unlock()
			}
		}(i)
	}
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	rep := &Report{
		SchemaVersion: SchemaVersion,
		Dataset:       cfg.Dataset,
		Budget:        cfg.Budget,
		Tokenizer:     tokens.Name,
		GoldTests:     cfg.Gold.IncludeTests,
		Bootstrap:     BootstrapInfo{Resamples: cfg.Resamples, Seed: cfg.Seed, Level: 0.95},
		Tasks:         len(sorted),
		Skipped:       []Skip{},
		Paired:        []Paired{},
	}
	per := make([][]TaskResult, len(rets))
	for _, o := range outcomes {
		if o.err != nil {
			rep.Skipped = append(rep.Skipped, Skip{ID: o.task.ID, Reason: o.err.Error()})
			continue
		}
		for b := range rets {
			per[b] = append(per[b], o.results[b])
		}
	}
	rep.Scored = len(sorted) - len(rep.Skipped)
	boot := NewBootstrap(rep.Scored, cfg.Resamples, cfg.Seed, 0.95)
	cols := make([]map[string][]float64, len(rets))
	for b, r := range rets {
		cols[b] = columns(per[b])
		br := BaselineReport{Name: r.Name(), Aggregate: map[string]Stat{}, Tasks: per[b]}
		if br.Tasks == nil {
			br.Tasks = []TaskResult{}
		}
		for _, m := range MetricNames {
			br.Aggregate[m] = boot.Mean(cols[b][m])
		}
		rep.Baselines = append(rep.Baselines, br)
	}
	for a := range rets {
		for b := a + 1; b < len(rets); b++ {
			for _, m := range MetricNames {
				mean, lo, hi, n := boot.Diff(cols[a][m], cols[b][m])
				rep.Paired = append(rep.Paired, Paired{
					A: rets[a].Name(), B: rets[b].Name(), Metric: m,
					MeanDiff: mean, Lo: lo, Hi: hi, N: n,
				})
			}
		}
	}
	return rep, nil
}

// columns turns per-task metric maps into per-metric vectors, NaN where
// a metric is undefined for a task.
func columns(rs []TaskResult) map[string][]float64 {
	out := map[string][]float64{}
	for _, m := range MetricNames {
		col := make([]float64, len(rs))
		for i, r := range rs {
			v, ok := r.Metrics[m]
			if !ok {
				v = math.NaN()
			}
			col[i] = v
		}
		out[m] = col
	}
	return out
}

func runTask(ctx context.Context, t Task, src SnapshotSource, rets []retrieve.Retriever, cfg RunConfig) taskOutcome {
	o := taskOutcome{task: t}
	snap, err := src.Snapshot(ctx, t.Repo, t.BaseCommit)
	if err != nil {
		o.err = err
		return o
	}
	if err := ResolveGold(&t, snap, cfg.Gold); err != nil {
		o.err = err
		return o
	}
	o.task = t
	if len(t.GoldFiles) == 0 {
		o.err = fmt.Errorf("no gold files")
		return o
	}
	corpus, err := retrieve.NewCorpus(snap)
	if err != nil {
		o.err = err
		return o
	}
	q := retrieve.Query{ProblemStatement: t.ProblemStatement}
	for _, r := range rets {
		res, err := r.Retrieve(ctx, corpus, q, cfg.Budget)
		if err != nil {
			o.err = fmt.Errorf("%s: %w", r.Name(), err)
			return o
		}
		o.results = append(o.results, Score(t, res))
	}
	return o
}

// ResolveGold fills GoldFiles and GoldFunctions from the gold patch when
// they are empty, reading base-side sources from snap.
func ResolveGold(t *Task, snap retrieve.Snapshot, opt GoldOptions) error {
	if len(t.GoldFiles) > 0 && t.GoldFunctions != nil {
		return nil
	}
	diffs, err := ParseUnifiedDiff(t.GoldPatch)
	if err != nil {
		return fmt.Errorf("gold patch: %w", err)
	}
	if len(t.GoldFiles) == 0 {
		t.GoldFiles, t.AddedFiles = GoldFiles(diffs, opt)
	}
	if t.GoldFunctions == nil {
		infos, err := snap.Files()
		if err != nil {
			return err
		}
		present := map[string]bool{}
		for _, fi := range infos {
			present[fi.Path] = true
		}
		var paths []string
		for _, d := range diffs {
			if !d.Created() && present[d.OldPath] && symbols.Language(d.OldPath) != "" &&
				(opt.IncludeTests || !IsTestPath(d.OldPath)) {
				paths = append(paths, d.OldPath)
			}
		}
		data, err := snap.ReadFiles(paths)
		if err != nil {
			return fmt.Errorf("read gold files: %w", err)
		}
		t.GoldFunctions = GoldFunctions(diffs, opt, func(p string) ([]byte, bool) {
			b, ok := data[p]
			return b, ok
		})
	}
	return nil
}

// MarshalReport renders a report as indented JSON with a final newline.
func MarshalReport(r *Report) ([]byte, error) {
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}
