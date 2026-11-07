package cli

import (
	"reflect"
	"strings"
	"testing"

	"github.com/ArpitK24/forge/internal/core"
)

// TestParseInterspersedFlags pins the argument shapes the README and
// --help advertise. The stdlib flag package stops at the first
// positional argument, so before this fix every flag placed after the
// prompt was silently dropped (or folded into the prompt) and only the
// first word of an unquoted multi-word prompt survived.
func TestParseInterspersedFlags(t *testing.T) {
	type want struct {
		prompt     string
		print      bool
		model      string
		apiKey     string
		maxTokens  int
		maxTurns   int
		verbose    bool
		perm       core.PermissionMode
		permSet    bool
		format     core.OutputFormat
		formatSet  bool
		pluginDirs []string
	}
	cases := []struct {
		name string
		args []string
		want want
	}{
		{
			name: "README: -p prompt --api-key",
			args: []string{"-p", "hi", "--api-key", "k"},
			want: want{prompt: "hi", print: true, apiKey: "k"},
		},
		{
			name: "README: -p prompt --permission-mode=accept-edits",
			args: []string{"-p", "list the Go files in this repo", "--permission-mode=accept-edits"},
			want: want{prompt: "list the Go files in this repo", print: true,
				perm: core.PermissionAcceptEdits, permSet: true},
		},
		{
			name: "README: -p prompt --output-format=stream-json",
			args: []string{"-p", "what time is it?", "--output-format=stream-json"},
			want: want{prompt: "what time is it?", print: true,
				format: core.OutputStreamJson, formatSet: true},
		},
		{
			name: "flag after prompt with separate value",
			args: []string{"-p", "fix tests", "--model", "x"},
			want: want{prompt: "fix tests", print: true, model: "x"},
		},
		{
			name: "dangerously-skip-permissions after prompt",
			args: []string{"-p", "fix tests", "--dangerously-skip-permissions"},
			want: want{prompt: "fix tests", print: true,
				perm: core.PermissionBypassPermissions, permSet: true},
		},
		{
			name: "output-format json after prompt",
			args: []string{"-p", "x", "--output-format", "json"},
			want: want{prompt: "x", print: true, format: core.OutputJson, formatSet: true},
		},
		{
			name: "unquoted multi-word prompt",
			args: []string{"hello", "world"},
			want: want{prompt: "hello world"},
		},
		{
			name: "multi-word prompt with flags on both sides",
			args: []string{"-v", "fix", "the", "--max-tokens", "100", "failing", "tests", "-p"},
			want: want{prompt: "fix the failing tests", print: true, verbose: true, maxTokens: 100},
		},
		{
			name: "bool flag does not swallow the next word",
			args: []string{"-p", "hello"},
			want: want{prompt: "hello", print: true},
		},
		{
			name: "explicit bool value after prompt",
			args: []string{"hi", "--print=false", "-v=true"},
			want: want{prompt: "hi", verbose: true},
		},
		{
			name: "repeatable flag around the prompt",
			args: []string{"--plugin-dir", "a", "go", "--plugin-dir=b"},
			want: want{prompt: "go", pluginDirs: []string{"a", "b"}},
		},
		{
			name: "double-dash ends flag parsing",
			args: []string{"-p", "--", "--model", "is", "a", "flag"},
			want: want{prompt: "--model is a flag", print: true},
		},
		{
			name: "flags before double-dash still apply",
			args: []string{"explain", "--max-turns", "3", "--", "-v", "flag"},
			want: want{prompt: "explain -v flag", maxTurns: 3},
		},
		{
			name: "double-dash as a flag value is not a terminator",
			args: []string{"--model", "--", "hi", "-v"},
			want: want{prompt: "hi", model: "--", verbose: true},
		},
		{
			name: "lone dash is a positional",
			args: []string{"-p", "-"},
			want: want{prompt: "-", print: true},
		},
		{
			name: "no args",
			args: nil,
			want: want{},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a, err := NewParser("forge", tc.args).Parse()
			if err != nil {
				t.Fatalf("Parse(%q): %v", tc.args, err)
			}
			got := want{
				prompt:     a.PositionalPrompt,
				print:      a.Print,
				model:      a.Model,
				apiKey:     a.APIKey,
				maxTokens:  a.MaxTokens,
				maxTurns:   a.MaxTurns,
				verbose:    a.Verbose,
				perm:       a.PermissionMode,
				permSet:    a.PermissionModeSet,
				format:     a.OutputFormat,
				formatSet:  a.OutputFormatSet,
				pluginDirs: a.PluginDirs,
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("Parse(%q)\n got  %+v\n want %+v", tc.args, got, tc.want)
			}
		})
	}
}

// TestParseInterspersedFlagErrors checks that flags after the prompt
// are validated, not silently treated as prompt text.
func TestParseInterspersedFlagErrors(t *testing.T) {
	cases := []struct {
		name    string
		args    []string
		errHint string
	}{
		{"unknown flag after prompt", []string{"hi", "--bogus"}, "bogus"},
		{"missing value after prompt", []string{"hi", "--model"}, "model"},
		{"invalid value after prompt", []string{"hi", "--max-turns", "0"}, "max-turns"},
		{"invalid output format after prompt", []string{"-p", "hi", "--output-format=xml"}, "output-format"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NewParser("forge", tc.args).Parse()
			if err == nil {
				t.Fatalf("Parse(%q): want error, got nil", tc.args)
			}
			if !strings.Contains(err.Error(), tc.errHint) {
				t.Errorf("Parse(%q) error = %q, want it to mention %q", tc.args, err, tc.errHint)
			}
		})
	}
}

// TestUsageMatchesImplementation guards against --help advertising
// things the binary does not do.
func TestUsageMatchesImplementation(t *testing.T) {
	u := Usage("forge")
	for _, s := range []string{
		"acp",               // no ACP subcommand exists; "forge acp" sends "acp" as a prompt
		"ANTHROPIC_API_KEY", // never read anywhere
	} {
		if strings.Contains(u, s) {
			t.Errorf("Usage advertises %q, which is not implemented", s)
		}
	}
	for _, s := range []string{
		"NVIDIA_API_KEY", // read by headless and the TUI
		"OPENAI_API_KEY", // read by headless and the TUI
		"FORGE_CWD",      // read by resolveCwd
		"\n  --  ",       // end-of-flags marker
	} {
		if !strings.Contains(u, s) {
			t.Errorf("Usage missing %q", s)
		}
	}
}
