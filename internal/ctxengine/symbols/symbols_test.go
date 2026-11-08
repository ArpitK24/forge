package symbols

import (
	"reflect"
	"testing"
)

func names(syms []Symbol) []string {
	var out []string
	for _, s := range syms {
		out = append(out, s.Name)
	}
	return out
}

func find(t *testing.T, syms []Symbol, name string) Symbol {
	t.Helper()
	for _, s := range syms {
		if s.Name == name {
			return s
		}
	}
	t.Fatalf("symbol %q not found in %v", name, names(syms))
	return Symbol{}
}

const goSrc = `package p

// Server serves.
type Server struct {
	addr string
}

type (
	A int
	B[T any] struct{ v T }
)

// Start starts.
func (s *Server) Start() error {
	f := func() {}
	f()
	return nil
}

func (b B[T]) Get() T { return b.v }

func helper(x int) int {
	return x
}
`

func TestExtractGo(t *testing.T) {
	syms := Extract("p/server.go", []byte(goSrc))
	want := []string{"Server", "A", "B", "Server.Start", "B.Get", "helper"}
	if got := names(syms); !reflect.DeepEqual(got, want) {
		t.Fatalf("names = %v, want %v", got, want)
	}
	if s := find(t, syms, "Server"); s.Start != 3 || s.End != 6 || s.Kind != KindType {
		t.Errorf("Server = %+v, want lines 3-6 (doc comment included)", s)
	}
	if s := find(t, syms, "Server.Start"); s.Start != 13 || s.End != 18 || s.Kind != KindMethod {
		t.Errorf("Server.Start = %+v, want 13-18", s)
	}
	if s := find(t, syms, "B"); s.Start != 10 || s.End != 10 {
		t.Errorf("B = %+v, want line 10 only (grouped type decl)", s)
	}
}

func TestExtractGoSyntaxError(t *testing.T) {
	// A partial AST still yields the declarations before the error.
	syms := Extract("x.go", []byte("package x\n\nfunc ok() {}\n\nfunc broken( {\n"))
	if len(syms) == 0 || syms[0].Name != "ok" {
		t.Fatalf("got %v, want ok first", names(syms))
	}
}

const pySrc = `import os


@decorator
def top(a,
        b):
    """Doc.

Dedented docstring line that must not close the function.
    """
    def inner():
        return 1
    return inner


class Outer(Base):
    x = 1

    @property
    def prop(self):
        return (1 +
2)

    async def run(self):
        s = '''
def fake():
'''
        return s

    class Inner:
        def deep(self): pass

def last(): return 1  # comment
# trailing comment
`

func TestExtractPython(t *testing.T) {
	syms := Extract("pkg/mod.py", []byte(pySrc))
	want := []string{"top", "Outer", "Outer.prop", "Outer.run", "Outer.Inner", "Outer.Inner.deep", "last"}
	if got := names(syms); !reflect.DeepEqual(got, want) {
		t.Fatalf("names = %v, want %v", got, want)
	}
	cases := map[string][2]int{
		"top":              {4, 13},
		"Outer":            {16, 31},
		"Outer.prop":       {19, 22},
		"Outer.run":        {24, 28},
		"Outer.Inner":      {30, 31},
		"Outer.Inner.deep": {31, 31},
		"last":             {33, 33},
	}
	for name, span := range cases {
		s := find(t, syms, name)
		if s.Start != span[0] || s.End != span[1] {
			t.Errorf("%s = %d-%d, want %d-%d", name, s.Start, s.End, span[0], span[1])
		}
	}
	if s := find(t, syms, "Outer.run"); s.Kind != KindMethod {
		t.Errorf("Outer.run kind = %s", s.Kind)
	}
}

func TestExtractPythonTabsAndCRLF(t *testing.T) {
	src := "class A:\r\n\tdef f(self):\r\n\t\treturn 1\r\n\r\nx = 2\r\n"
	syms := Extract("a.py", []byte(src))
	if got := names(syms); !reflect.DeepEqual(got, []string{"A", "A.f"}) {
		t.Fatalf("names = %v", got)
	}
	if s := find(t, syms, "A"); s.End != 3 {
		t.Errorf("A ends at %d, want 3", s.End)
	}
}

func TestExtractUnsupported(t *testing.T) {
	if Extract("README.md", []byte("# hi")) != nil {
		t.Fatal("markdown should have no symbols")
	}
}

func TestEnclosing(t *testing.T) {
	syms := Extract("pkg/mod.py", []byte(pySrc))
	if s, ok := Enclosing(syms, 21); !ok || s.Name != "Outer.prop" {
		t.Errorf("line 21 -> %v %v, want Outer.prop", s.Name, ok)
	}
	if s, ok := Enclosing(syms, 17); !ok || s.Name != "Outer" {
		t.Errorf("line 17 -> %v, want Outer (class body)", s.Name)
	}
	if s, ok := Enclosing(syms, 11); !ok || s.Name != "top" {
		t.Errorf("line 11 -> %v, want top (nested def folds into parent)", s.Name)
	}
	if _, ok := Enclosing(syms, 1); ok {
		t.Error("module-level import should have no symbol")
	}
	// Insertion right after a function's last line is outside it.
	if s, ok := EnclosingInsertion(syms, 13); ok {
		t.Errorf("insertion after line 13 -> %v, want none", s.Name)
	}
	if s, ok := EnclosingInsertion(syms, 20); !ok || s.Name != "Outer.prop" {
		t.Errorf("insertion after line 20 -> %v, want Outer.prop", s.Name)
	}
}

func TestSplitIdent(t *testing.T) {
	cases := map[string][]string{
		"parseHTTPRequest":     {"parse", "http", "request"},
		"HTTPServer_closeAll2": {"http", "server", "close", "all2"},
		"__init__":             {"init"},
		"snake_case_name":      {"snake", "case", "name"},
		"X":                    {"x"},
		"Base64Encode":         {"base64", "encode"},
	}
	for in, want := range cases {
		if got := SplitIdent(in); !reflect.DeepEqual(got, want) {
			t.Errorf("SplitIdent(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestTerms(t *testing.T) {
	got := Terms("The RunQueryLoop drops tool_result blocks in 2 cases.")
	want := []string{"run", "query", "loop", "runqueryloop", "drops", "tool", "result", "tool_result", "blocks", "cases"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Terms = %v, want %v", got, want)
	}
}

func TestLanguage(t *testing.T) {
	for p, want := range map[string]string{"a/b.go": "go", "x.PY": "python", "s.pyi": "python", "c.rs": ""} {
		if got := Language(p); got != want {
			t.Errorf("Language(%q) = %q, want %q", p, got, want)
		}
	}
}
