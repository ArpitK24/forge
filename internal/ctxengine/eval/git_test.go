package eval

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	base := []string{"-C", dir, "-c", "user.name=Test", "-c", "user.email=test@example.com",
		"-c", "commit.gpgsign=false", "-c", "core.autocrlf=false"}
	cmd := exec.Command("git", append(base, args...)...)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_AUTHOR_DATE=2026-01-01T00:00:00Z", "GIT_COMMITTER_DATE=2026-01-01T00:00:00Z")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func write(t *testing.T, dir, rel, content string) {
	t.Helper()
	p := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// fixtureRepo builds: base (x.go + x_test.go), a qualifying fix of
// Greeter.Hello with its test, and a README-only commit.
func fixtureRepo(t *testing.T) (dir, base, fix string) {
	requireGit(t)
	dir = t.TempDir()
	git(t, dir, "init", "-q")
	write(t, dir, "internal/x/x.go", goBase)
	write(t, dir, "internal/x/x_test.go", "package x\n\nfunc TestX() {}\n")
	write(t, dir, "CHANGELOG.md", "# Changes\n")
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-q", "-m", "initial")
	base = git(t, dir, "rev-parse", "HEAD")
	write(t, dir, "internal/x/x.go", strings.Replace(goBase, `"hello %s"`, `"hello, %s"`, 1))
	write(t, dir, "internal/x/x_test.go", "package x\n\nfunc TestX() { _ = 1 }\n")
	write(t, dir, "CHANGELOG.md", "# Changes\n- comma\n")
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-q", "-m", "fix(x): Hello uses a comma (#5)\n\nThe greeting in internal/x/x.go lacked a comma; see x.go.\n\nCo-authored-by: A <a@example.com>")
	fix = git(t, dir, "rev-parse", "HEAD")
	write(t, dir, "README.md", "hi\n")
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-q", "-m", "docs: readme")
	return dir, base, fix
}

func TestGitSnapshot(t *testing.T) {
	dir, base, _ := fixtureRepo(t)
	rc := &RepoCache{Local: map[string]string{"o/r": dir}, NoFetch: true}
	snap, err := rc.Snapshot(context.Background(), "o/r", base)
	if err != nil {
		t.Fatal(err)
	}
	files, err := snap.Files()
	if err != nil || len(files) != 3 || files[1].Path != "internal/x/x.go" || files[1].Size != int64(len(goBase)) {
		t.Fatalf("files = %+v, %v", files, err)
	}
	data, err := snap.ReadFiles([]string{"internal/x/x.go", "CHANGELOG.md"})
	if err != nil || string(data["internal/x/x.go"]) != goBase || string(data["CHANGELOG.md"]) != "# Changes\n" {
		t.Fatalf("read = %q, %v", data, err)
	}
	if _, err := snap.ReadFiles([]string{"README.md"}); err == nil {
		t.Fatal("README.md does not exist at base")
	}
	if _, err := rc.Snapshot(context.Background(), "o/r", strings.Repeat("0", 40)); err == nil {
		t.Fatal("missing commit accepted with NoFetch")
	}
}

func TestRepoCacheClones(t *testing.T) {
	src, base, _ := fixtureRepo(t)
	rc := &RepoCache{Dir: t.TempDir(), RemoteURL: func(string) string { return src }}
	snap, err := rc.Snapshot(context.Background(), "o/r", base)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(rc.Dir, "repos", "o__r.git", "HEAD")); err != nil {
		t.Fatalf("bare clone missing: %v", err)
	}
	if _, err := snap.Files(); err != nil {
		t.Fatal(err)
	}
	if _, err := rc.GitDir(context.Background(), "../evil"); err == nil {
		t.Fatal("bad repo name accepted")
	}
	off := &RepoCache{Dir: t.TempDir(), NoFetch: true}
	if _, err := off.GitDir(context.Background(), "o/r"); err == nil {
		t.Fatal("clone attempted with NoFetch")
	}
}

func TestMineGitTasks(t *testing.T) {
	dir, base, fix := fixtureRepo(t)
	tasks, err := MineGitTasks(context.Background(), dir, MineOptions{Repo: "o/r"})
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 {
		t.Fatalf("got %d tasks, want 1: %+v", len(tasks), tasks)
	}
	tk := tasks[0]
	if tk.ID != "forge-"+fix[:12] || tk.BaseCommit != base || tk.FixCommit != fix || tk.Split != SplitInternal {
		t.Fatalf("task = %+v", tk)
	}
	if !reflect.DeepEqual(tk.GoldFiles, []string{"internal/x/x.go"}) || !reflect.DeepEqual(tk.GoldFunctions, []string{"internal/x/x.go::Greeter.Hello"}) {
		t.Fatalf("gold = %v %v", tk.GoldFiles, tk.GoldFunctions)
	}
	if want := "Hello uses a comma\n\nThe greeting in <file> lacked a comma; see <file>."; tk.ProblemStatement != want {
		t.Fatalf("statement = %q\nwant        %q", tk.ProblemStatement, want)
	}
	if strings.Contains(tk.GoldPatch, "CHANGELOG") || strings.Contains(tk.GoldPatch, "x_test.go") || !strings.Contains(tk.GoldPatch, "hello, %s") {
		t.Fatalf("gold patch not filtered to gold files:\n%s", tk.GoldPatch)
	}
}

func TestCLIRetrieval(t *testing.T) {
	dir, _, _ := fixtureRepo(t)
	tasks, err := MineGitTasks(context.Background(), dir, MineOptions{Repo: "o/r"})
	if err != nil {
		t.Fatal(err)
	}
	tmp := t.TempDir()
	tasksPath := filepath.Join(tmp, "tasks.jsonl")
	if err := WriteTasks(tasksPath, tasks); err != nil {
		t.Fatal(err)
	}
	var outs [][]byte
	for i := 0; i < 2; i++ {
		out := filepath.Join(tmp, "r.json")
		var stdout, stderr bytes.Buffer
		code := Main(context.Background(), []string{"retrieval", "--tasks", tasksPath, "--repo", "o/r=" + dir,
			"--no-fetch", "--strict", "--budget", "2000", "--bootstrap", "50", "--out", out}, &stdout, &stderr)
		if code != 0 {
			t.Fatalf("exit %d: %s", code, stderr.String())
		}
		if !strings.Contains(stderr.String(), "| b2 |") {
			t.Fatalf("no summary table: %s", stderr.String())
		}
		b, _ := os.ReadFile(out)
		outs = append(outs, b)
	}
	if !bytes.Equal(outs[0], outs[1]) {
		t.Fatal("CLI output differs between runs")
	}
	var rep Report
	if err := json.Unmarshal(outs[0], &rep); err != nil || rep.Scored != 1 || len(rep.Baselines) != 4 {
		t.Fatalf("report = %+v, %v", rep, err)
	}
}

func TestCLIErrors(t *testing.T) {
	var out, errb bytes.Buffer
	if code := Main(context.Background(), nil, &out, &errb); code != 2 || !strings.Contains(errb.String(), "forge eval retrieval") {
		t.Fatalf("no args: %d %q", code, errb.String())
	}
	if code := Main(context.Background(), []string{"--help"}, &out, &errb); code != 0 {
		t.Fatalf("--help: %d", code)
	}
	if code := Main(context.Background(), []string{"bogus"}, &out, &errb); code != 2 {
		t.Fatalf("bogus: %d", code)
	}
	if code := Main(context.Background(), []string{"retrieval", "--baseline", "b7"}, &out, &errb); code != 2 {
		t.Fatalf("bad baseline: %d", code)
	}
	if code := Main(context.Background(), []string{"retrieval", "--dataset", "nope", "--eval-dir", t.TempDir()}, &out, &errb); code != 2 {
		t.Fatalf("bad dataset: %d", code)
	}
	if code := Main(context.Background(), []string{"retrieval", "-h"}, &out, &errb); code != 0 {
		t.Fatalf("retrieval -h: %d", code)
	}
}

func TestSanitizeCommitMessage(t *testing.T) {
	got := SanitizeCommitMessage("feat(core)!: add Foo to core/foo.go (#12)\n\nSee foo.go and myfoo.go.\nSigned-off-by: X <x@y>\n", []string{"core/foo.go"})
	if want := "add Foo to <file>\n\nSee <file> and myfoo.go."; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	got = SanitizeCommitMessage("move framing into internal/plugins/ and internal/plugins, not internal/pluginsx", []string{"internal/plugins/plugin.go"})
	if want := "move framing into <file> and <file>, not internal/pluginsx"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}
