package eval

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestDevSplit(t *testing.T) {
	var tasks []Task
	add := func(repo string, n int) {
		for i := 0; i < n; i++ {
			tasks = append(tasks, Task{ID: fmt.Sprintf("%s-%03d", repo, i), Repo: repo})
		}
	}
	add("big", 230)
	add("mid", 50)
	add("small", 15)
	add("tiny", 5)
	ids := DevSplit(tasks, 100, 42)
	if len(ids) != 100 {
		t.Fatalf("len = %d", len(ids))
	}
	count := map[string]int{}
	for _, id := range ids {
		count[strings.SplitN(id, "-", 2)[0]]++
	}
	// 300 tasks -> quotas 76.67, 16.67, 5, 1.67 -> 77, 17, 5, 1 by
	// largest remainder (big and mid tie at .67 before tiny's .67? all
	// three remainders are 200/300; ties break by repo name).
	if want := map[string]int{"big": 77, "mid": 17, "small": 5, "tiny": 1}; !reflect.DeepEqual(count, want) {
		t.Fatalf("strata = %v, want %v", count, want)
	}
	if again := DevSplit(tasks, 100, 42); !reflect.DeepEqual(again, ids) {
		t.Fatal("same seed, different split")
	}
	if other := DevSplit(tasks, 100, 43); reflect.DeepEqual(other, ids) {
		t.Fatal("different seed, same split")
	}
	if all := DevSplit(tasks[:3], 100, 1); len(all) != 3 {
		t.Fatalf("n > total: %v", all)
	}
}

func TestFetchAndLoadSWEBench(t *testing.T) {
	rows := []SWEBenchRow{
		{InstanceID: "o__r-2", Repo: "o/r", BaseCommit: "c2", Patch: fixturePatch, ProblemStatement: "two"},
		{InstanceID: "o__r-1", Repo: "o/r", BaseCommit: "c1", Patch: fixturePatch, ProblemStatement: "one"},
		{InstanceID: "o__r-3", Repo: "o/r", BaseCommit: "c3", Patch: fixturePatch, ProblemStatement: "three"},
	}
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			w.WriteHeader(http.StatusServiceUnavailable) // retried
			return
		}
		q := r.URL.Query()
		if q.Get("dataset") != "princeton-nlp/SWE-bench_Lite" || q.Get("split") != "test" {
			t.Errorf("query = %v", q)
		}
		var off int
		fmt.Sscan(q.Get("offset"), &off)
		type wrapped struct {
			Row SWEBenchRow `json:"row"`
		}
		page := struct {
			Rows  []wrapped `json:"rows"`
			Total int       `json:"num_rows_total"`
		}{Total: len(rows)}
		if off < len(rows) {
			page.Rows = append(page.Rows, wrapped{rows[off]})
		}
		_ = json.NewEncoder(w).Encode(page)
	}))
	defer srv.Close()

	old := hfRowsEndpoint
	hfRowsEndpoint = srv.URL
	defer func() { hfRowsEndpoint = old }()

	dir := t.TempDir()
	// The fake server returns one row per page; FetchSWEBench pages by
	// 100, so it must use the returned rows rather than assume 100.
	p, err := FetchSWEBench(context.Background(), SWEBenchLite, dir, srv.Client(), &strings.Builder{})
	if err == nil {
		// Paging by 100 with a 1-row page means only one row arrives:
		// the row-count check must catch it.
		t.Fatalf("short download accepted: %s", p)
	}

	pageSize = 1
	defer func() { pageSize = 100 }()
	p, err = FetchSWEBench(context.Background(), SWEBenchLite, dir, srv.Client(), &strings.Builder{})
	if err != nil {
		t.Fatal(err)
	}
	if p != filepath.Join(dir, "datasets", "swe-bench-lite.jsonl") {
		t.Fatalf("path = %s", p)
	}
	tasks, err := LoadSWEBench(p, SWEBenchLite, map[string]bool{"o__r-2": true})
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 3 || tasks[0].ID != "o__r-1" || tasks[1].Split != SplitDev || tasks[0].Split != SplitHeldout {
		t.Fatalf("tasks = %+v", tasks)
	}
	// SWE-bench gold keeps everything in the patch (its tests are in a
	// separate test_patch), including the x_test.go fixture section.
	if !reflect.DeepEqual(tasks[0].GoldFiles, []string{"img.png", "internal/x/x.go", "internal/x/x_test.go", "old.txt", "pkg/mod.py"}) {
		t.Fatalf("gold = %v", tasks[0].GoldFiles)
	}
	if tasks[0].GoldFunctions != nil {
		t.Fatal("gold functions should resolve lazily")
	}
	if _, err := LoadSWEBench(filepath.Join(dir, "nope.jsonl"), SWEBenchLite, nil); err == nil || !strings.Contains(err.Error(), "fetch-swebench") {
		t.Fatalf("missing cache error = %v", err)
	}
}

func TestReadIDList(t *testing.T) {
	p := filepath.Join(t.TempDir(), "ids.txt")
	if err := os.WriteFile(p, []byte("# comment\na\n\n b \n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ids, err := ReadIDList(p)
	if err != nil || !reflect.DeepEqual(ids, map[string]bool{"a": true, "b": true}) {
		t.Fatalf("ids = %v, %v", ids, err)
	}
}

func TestTaskRoundTrip(t *testing.T) {
	tasks := []Task{{ID: "b", GoldFiles: []string{"x"}, GoldFunctions: []string{}}, {ID: "a", ProblemStatement: "<html> & stuff"}}
	p := filepath.Join(t.TempDir(), "t.jsonl")
	if err := WriteTasks(p, tasks); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(p)
	if !strings.Contains(string(b), "<html> & stuff") {
		t.Fatalf("HTML escaped: %s", b)
	}
	got, err := ReadTasks(p)
	if err != nil || len(got) != 2 || got[0].ID != "a" || got[1].GoldFunctions == nil {
		t.Fatalf("round trip = %+v, %v", got, err)
	}
}
