// Package eval is the context engine's retrieval-only evaluation
// harness (design doc section 13, Milestone 0): the task schema, gold
// extraction from patches, the SWE-bench and internal Forge dataset
// loaders, retrieval metrics with paired bootstrap confidence
// intervals, and the hidden `forge eval` command.
//
// Everything is deterministic: no model calls, no clocks, no map-order
// dependence. Two runs over the same inputs produce byte-identical JSON.
package eval

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
)

// Splits.
const (
	SplitDev      = "dev"      // SWE-bench Verified dev split (100 tasks)
	SplitHeldout  = "heldout"  // everything else from SWE-bench; milestone gates only
	SplitInternal = "internal" // tasks mined from Forge's own history
)

// Task is one retrieval task. It is stored one JSON object per line.
type Task struct {
	ID               string `json:"id"`
	Repo             string `json:"repo"` // "owner/name" on GitHub
	BaseCommit       string `json:"base_commit"`
	ProblemStatement string `json:"problem_statement"`
	GoldPatch        string `json:"gold_patch"`
	// GoldFiles are the non-test files the gold patch modifies or
	// deletes, i.e. files that exist at BaseCommit.
	GoldFiles []string `json:"gold_files"`
	// GoldFunctions are "path::Symbol" ids of the symbols enclosing the
	// patch's hunks at BaseCommit. Empty until resolved against the
	// base snapshot (the harness does that on the fly).
	GoldFunctions []string `json:"gold_functions"`
	Split         string   `json:"split"`
	Source        string   `json:"source"`

	// AddedFiles are non-test files the patch creates. They cannot be
	// retrieved at the base commit, so they are not scored.
	AddedFiles []string `json:"added_files,omitempty"`
	FixCommit  string   `json:"fix_commit,omitempty"`
	CreatedAt  string   `json:"created_at,omitempty"`
}

// ReadTasks reads a JSONL task file.
func ReadTasks(path string) ([]Task, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return DecodeTasks(f)
}

// DecodeTasks reads JSONL tasks from r, skipping blank lines.
func DecodeTasks(r io.Reader) ([]Task, error) {
	var out []Task
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 1<<20), 64<<20)
	line := 0
	for sc.Scan() {
		line++
		b := bytes.TrimSpace(sc.Bytes())
		if len(b) == 0 {
			continue
		}
		var t Task
		if err := json.Unmarshal(b, &t); err != nil {
			return nil, fmt.Errorf("line %d: %w", line, err)
		}
		out = append(out, t)
	}
	return out, sc.Err()
}

// WriteTasks writes tasks as JSONL, sorted by ID.
func WriteTasks(path string, tasks []Task) error {
	var buf bytes.Buffer
	if err := EncodeTasks(&buf, tasks); err != nil {
		return err
	}
	return os.WriteFile(path, buf.Bytes(), 0o644)
}

// EncodeTasks writes tasks as JSONL, sorted by ID.
func EncodeTasks(w io.Writer, tasks []Task) error {
	sorted := append([]Task(nil), tasks...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].ID < sorted[j].ID })
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	for _, t := range sorted {
		if err := enc.Encode(t); err != nil {
			return err
		}
	}
	return nil
}
