package plugins

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/ArpitK24/forge/internal/core"
)

func TestLoadPlugins_MissingDirIsNotAnError(t *testing.T) {
	cfg := &core.Config{PluginDirs: []string{filepath.Join(t.TempDir(), "does-not-exist")}}
	got, err := LoadPlugins(context.Background(), cfg, nil)
	if err != nil || len(got) != 0 {
		t.Fatalf("LoadPlugins = %v, %v; want no tools, nil error", got, err)
	}
}

func TestLoadPlugins_SkipsBrokenPlugins(t *testing.T) {
	root := t.TempDir()
	mk := func(name, manifest string) string {
		d := filepath.Join(root, name)
		os.MkdirAll(d, 0o755)
		if manifest != "" {
			os.WriteFile(filepath.Join(d, "plugin.json"), []byte(manifest), 0o644)
		}
		return d
	}
	mk("no-manifest", "")
	mk("bad-json", `{oops`)
	mk("no-entry-file", `{"name":"ghost","entrypoint":"ghost-bin"}`)
	// Entrypoint exists but isn't a plugin: it exits immediately,
	// so initialize must fail and the plugin is skipped, not hung.
	notPlugin := mk("not-a-plugin", `{"name":"liar","entrypoint":"liar"}`)
	writeExitingBinary(t, notPlugin, "liar")
	// A stray file at the root is ignored.
	os.WriteFile(filepath.Join(root, "README.txt"), []byte("hi"), 0o644)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	got, err := LoadPlugins(ctx, &core.Config{PluginDirs: []string{root}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("got %d tools from broken plugins, want 0", len(got))
	}
}

// writeExitingBinary drops an executable that exits at once.
func writeExitingBinary(t *testing.T, dir, name string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		// cmd /c exit is not a standalone .exe; copy a tiny built binary instead.
		src := filepath.Join(dir, "main.go")
		os.WriteFile(src, []byte("package main\nfunc main() {}\n"), 0o644)
		goBuild(t, dir, src, filepath.Join(dir, name+".exe"))
		return
	}
	os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\nexit 0\n"), 0o755)
}

func goBuild(t *testing.T, workdir, pkg, out string) {
	t.Helper()
	gobin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go toolchain not on PATH")
	}
	cmd := exec.Command(gobin, "build", "-o", out, pkg)
	cmd.Dir = workdir
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build %s: %v\n%s", pkg, err, b)
	}
}

// TestLoadPlugins_EchoExampleEndToEnd builds examples/plugins/echo,
// installs it with its shipped plugin.json, and drives it through
// LoadPlugins -> initialize -> tools/call over a real subprocess.
// Before the framing fix this loaded zero tools.
func TestLoadPlugins_EchoExampleEndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and spawns a subprocess")
	}
	repoRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	exampleDir := filepath.Join(repoRoot, "examples", "plugins", "echo")
	manifestBytes, err := os.ReadFile(filepath.Join(exampleDir, "plugin.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest PluginManifest
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		t.Fatal(err)
	}

	root := t.TempDir()
	pdir := filepath.Join(root, "echo")
	os.MkdirAll(pdir, 0o755)
	os.WriteFile(filepath.Join(pdir, "plugin.json"), manifestBytes, 0o644)
	bin := filepath.Join(pdir, manifest.Entrypoint)
	if runtime.GOOS == "windows" && !strings.HasSuffix(bin, ".exe") {
		bin += ".exe"
	}
	goBuild(t, repoRoot, "./examples/plugins/echo", bin)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	loaded, err := LoadPlugins(ctx, &core.Config{PluginDirs: []string{root}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded) != 1 {
		t.Fatalf("loaded %d tools, want 1 (the echo tool)", len(loaded))
	}
	tool := loaded[0].(*PluginTool)
	defer tool.Client().Close()

	if tool.Name() != "plugin__echo__echo" {
		t.Fatalf("Name() = %q", tool.Name())
	}
	if tool.Description() != "Echo back the input text" {
		t.Fatalf("Description() = %q; plugin descriptions must reach the model", tool.Description())
	}
	if !strings.Contains(string(tool.InputSchema()), `"text"`) {
		t.Fatalf("InputSchema() = %s; plugin schema must reach the model", tool.InputSchema())
	}

	res := tool.Execute(ctx, json.RawMessage(`{"text":"hello from a real subprocess"}`), nil)
	if res.IsError || res.Text != "hello from a real subprocess" {
		t.Fatalf("Execute = %+v", res)
	}
	res = tool.Execute(ctx, json.RawMessage(`{"text":42}`), nil)
	if !res.IsError || !strings.Contains(res.Text, "invalid input") {
		t.Fatalf("bad input should come back as a tool error: %+v", res)
	}
}
