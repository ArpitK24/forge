package eval

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// SWEBenchDataset describes one SWE-bench variant on Hugging Face.
type SWEBenchDataset struct {
	Name   string // "lite" or "verified"
	HFName string // Hugging Face dataset id
	Source string // Task.Source value
	Rows   int    // expected row count
}

// SWE-bench variants. Both have a single "default" config with a "test"
// split.
var (
	SWEBenchLite     = SWEBenchDataset{Name: "lite", HFName: "princeton-nlp/SWE-bench_Lite", Source: "swe-bench-lite", Rows: 300}
	SWEBenchVerified = SWEBenchDataset{Name: "verified", HFName: "princeton-nlp/SWE-bench_Verified", Source: "swe-bench-verified", Rows: 500}
)

// HFRowsEndpoint is the Hugging Face datasets-server rows API. It pages
// at most 100 rows per request.
const HFRowsEndpoint = "https://datasets-server.huggingface.co/rows"

// Overridable in tests.
var (
	hfRowsEndpoint = HFRowsEndpoint
	pageSize       = 100
)

// SWEBenchRow is the subset of a SWE-bench row the harness keeps.
// hints_text, FAIL_TO_PASS and PASS_TO_PASS are dropped on purpose:
// hints often name the fix and the test lists are hidden-test leakage
// for a retrieval eval.
type SWEBenchRow struct {
	InstanceID       string `json:"instance_id"`
	Repo             string `json:"repo"`
	BaseCommit       string `json:"base_commit"`
	Patch            string `json:"patch"`
	ProblemStatement string `json:"problem_statement"`
	CreatedAt        string `json:"created_at"`
	Version          string `json:"version"`
}

// CachePath is where FetchSWEBench stores a dataset.
func (d SWEBenchDataset) CachePath(cacheDir string) string {
	return filepath.Join(cacheDir, "datasets", "swe-bench-"+d.Name+".jsonl")
}

// FetchSWEBench downloads every row of d into its cache path (JSONL,
// sorted by instance id). The download is never committed.
func FetchSWEBench(ctx context.Context, d SWEBenchDataset, cacheDir string, client *http.Client, log io.Writer) (string, error) {
	if client == nil {
		client = &http.Client{Timeout: 60 * time.Second}
	}
	var rows []SWEBenchRow
	total := -1
	for offset := 0; total < 0 || offset < total; offset += pageSize {
		q := url.Values{
			"dataset": {d.HFName}, "config": {"default"}, "split": {"test"},
			"offset": {fmt.Sprint(offset)}, "length": {fmt.Sprint(pageSize)},
		}
		var page struct {
			Rows []struct {
				Row SWEBenchRow `json:"row"`
			} `json:"rows"`
			NumRowsTotal int `json:"num_rows_total"`
		}
		if err := getJSON(ctx, client, hfRowsEndpoint+"?"+q.Encode(), &page); err != nil {
			return "", fmt.Errorf("%s offset %d: %w", d.HFName, offset, err)
		}
		total = page.NumRowsTotal
		if len(page.Rows) == 0 {
			break
		}
		for _, r := range page.Rows {
			rows = append(rows, r.Row)
		}
		fmt.Fprintf(log, "%s: %d/%d rows\n", d.HFName, len(rows), total)
	}
	if len(rows) != total {
		return "", fmt.Errorf("%s: got %d rows, server reports %d", d.HFName, len(rows), total)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].InstanceID < rows[j].InstanceID })
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	for _, r := range rows {
		if err := enc.Encode(r); err != nil {
			return "", err
		}
	}
	p := d.CachePath(cacheDir)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return "", err
	}
	return p, os.WriteFile(p, buf.Bytes(), 0o644)
}

func getJSON(ctx context.Context, c *http.Client, u string, v any) error {
	var last error
	for attempt := 0; attempt < 5; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Duration(attempt*attempt) * time.Second):
			}
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return err
		}
		resp, err := c.Do(req)
		if err != nil {
			last = err
			continue
		}
		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			last = err
			continue
		}
		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
			last = fmt.Errorf("HTTP %d", resp.StatusCode)
			continue
		}
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body[:min(len(body), 200)])))
		}
		return json.Unmarshal(body, v)
	}
	return last
}

// LoadSWEBench reads a cached dataset and converts it to tasks. Tasks
// whose id is in devIDs get split "dev", the rest "heldout". Gold files
// come from the patch as-is (SWE-bench already separates the test
// patch); gold functions are resolved later against the base commit.
func LoadSWEBench(path string, d SWEBenchDataset, devIDs map[string]bool) ([]Task, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("%w (run `forge eval fetch-swebench --dataset %s` first)", err, d.Name)
	}
	defer f.Close()
	var out []Task
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1<<20), 64<<20)
	for sc.Scan() {
		if len(bytes.TrimSpace(sc.Bytes())) == 0 {
			continue
		}
		var r SWEBenchRow
		if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
			return nil, err
		}
		diffs, err := ParseUnifiedDiff(r.Patch)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", r.InstanceID, err)
		}
		gold, added := GoldFiles(diffs, GoldOptions{IncludeTests: true})
		split := SplitHeldout
		if devIDs[r.InstanceID] {
			split = SplitDev
		}
		out = append(out, Task{
			ID:               r.InstanceID,
			Repo:             r.Repo,
			BaseCommit:       r.BaseCommit,
			ProblemStatement: r.ProblemStatement,
			GoldPatch:        r.Patch,
			GoldFiles:        gold,
			AddedFiles:       added,
			Split:            split,
			Source:           d.Source,
			CreatedAt:        r.CreatedAt,
		})
	}
	return out, sc.Err()
}

// DevSplit draws n task ids stratified by repo with a fixed seed:
// repos get largest-remainder quotas proportional to their size (ties
// broken by repo name), and each repo's ids are shuffled with a
// SplitMix64 stream before taking its quota. The result is sorted.
func DevSplit(tasks []Task, n int, seed uint64) []string {
	byRepo := map[string][]string{}
	for _, t := range tasks {
		byRepo[t.Repo] = append(byRepo[t.Repo], t.ID)
	}
	repos := make([]string, 0, len(byRepo))
	for r := range byRepo {
		repos = append(repos, r)
		sort.Strings(byRepo[r])
	}
	sort.Strings(repos)
	total := len(tasks)
	if n > total {
		n = total
	}
	type quota struct {
		repo string
		q    int
		rem  int // remainder numerator, compared exactly
	}
	qs := make([]quota, len(repos))
	used := 0
	for i, r := range repos {
		num := n * len(byRepo[r])
		qs[i] = quota{repo: r, q: num / total, rem: num % total}
		used += qs[i].q
	}
	order := make([]int, len(qs))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool {
		if qs[order[a]].rem != qs[order[b]].rem {
			return qs[order[a]].rem > qs[order[b]].rem
		}
		return qs[order[a]].repo < qs[order[b]].repo
	})
	for k := 0; used < n; k++ {
		qs[order[k%len(order)]].q++
		used++
	}
	rng := splitMix(seed)
	var out []string
	for _, q := range qs {
		ids := append([]string(nil), byRepo[q.repo]...)
		for i := len(ids) - 1; i > 0; i-- { // Fisher-Yates
			j := int(rng() % uint64(i+1))
			ids[i], ids[j] = ids[j], ids[i]
		}
		out = append(out, ids[:q.q]...)
	}
	sort.Strings(out)
	return out
}

// ReadIDList reads one id per line, ignoring blanks and # comments.
func ReadIDList(path string) (map[string]bool, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	out := map[string]bool{}
	for _, l := range strings.Split(string(b), "\n") {
		l = strings.TrimSpace(l)
		if l != "" && !strings.HasPrefix(l, "#") {
			out[l] = true
		}
	}
	return out, nil
}
