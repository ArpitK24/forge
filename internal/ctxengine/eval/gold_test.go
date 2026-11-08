package eval

import (
	"reflect"
	"testing"
)

// goBase is internal/x/x.go at the base commit of fixturePatch.
const goBase = `package x

import "fmt"

// Greeter greets.
type Greeter struct {
	name string
}

func (g *Greeter) Hello() string {
	return fmt.Sprintf("hello %s", g.name)
}

func Bye() string {
	return "bye"
}
`

// pyBase is pkg/mod.py at the base commit of fixturePatch.
const pyBase = `import os


class Config:
    debug = False

    def load(self, path):
        with open(path) as f:
            return f.read()


def main():
    print("hi")
`

const fixturePatch = `diff --git a/internal/x/x.go b/internal/x/x.go
index 1111111..2222222 100644
--- a/internal/x/x.go
+++ b/internal/x/x.go
@@ -9,6 +9,6 @@ type Greeter struct {
 
 func (g *Greeter) Hello() string {
-	return fmt.Sprintf("hello %s", g.name)
+	return fmt.Sprintf("hello, %s", g.name)
 }
 
 func Bye() string {
@@ -16,0 +17,4 @@ func Bye() string {
+
+func New() *Greeter {
+	return &Greeter{}
+}
diff --git a/internal/x/x_test.go b/internal/x/x_test.go
--- a/internal/x/x_test.go
+++ b/internal/x/x_test.go
@@ -1,3 +1,3 @@
 package x
 
-func TestX() {}
+func TestX() { _ = 1 }
diff --git a/pkg/mod.py b/pkg/mod.py
--- a/pkg/mod.py
+++ b/pkg/mod.py
@@ -6,6 +6,7 @@ class Config:
 
     def load(self, path):
         with open(path) as f:
+            # read it all
             return f.read()
 
 
@@ -1,4 +1,5 @@
 import os
+import sys
 
 
 class Config:
diff --git a/pkg/new.py b/pkg/new.py
new file mode 100644
index 0000000..3333333
--- /dev/null
+++ b/pkg/new.py
@@ -0,0 +1,2 @@
+def fresh():
+    pass
diff --git a/old.txt b/old.txt
deleted file mode 100644
--- a/old.txt
+++ /dev/null
@@ -1,2 +0,0 @@
--- this line looks like a header but is content
-bye
diff --git a/img.png b/img.png
index 1..2 100644
Binary files a/img.png and b/img.png differ
`

func TestParseUnifiedDiff(t *testing.T) {
	diffs, err := ParseUnifiedDiff(fixturePatch)
	if err != nil {
		t.Fatal(err)
	}
	if len(diffs) != 6 {
		t.Fatalf("got %d file diffs, want 6", len(diffs))
	}
	x := diffs[0]
	if x.OldPath != "internal/x/x.go" || !reflect.DeepEqual(x.Changed, []int{11}) {
		t.Errorf("x.go = %+v, want line 11 changed", x)
	}
	if !reflect.DeepEqual(x.InsertAfter, []int{11, 16, 16, 16, 16}) {
		t.Errorf("x.go inserts = %v", x.InsertAfter)
	}
	if !diffs[3].Created() || diffs[3].NewPath != "pkg/new.py" {
		t.Errorf("new.py = %+v", diffs[3])
	}
	del := diffs[4]
	if del.OldPath != "old.txt" || del.NewPath != "" || !reflect.DeepEqual(del.Changed, []int{1, 2}) {
		t.Errorf("deleted = %+v", del)
	}
	if !diffs[5].Binary {
		t.Errorf("img.png not binary: %+v", diffs[5])
	}
}

func TestParseUnifiedDiffRename(t *testing.T) {
	diffs, err := ParseUnifiedDiff("diff --git a/a.go b/b.go\nsimilarity index 90%\nrename from a.go\nrename to b.go\n--- a/a.go\n+++ b/b.go\n@@ -1 +1 @@\n-x\n+y\n")
	if err != nil {
		t.Fatal(err)
	}
	if len(diffs) != 1 || diffs[0].OldPath != "a.go" || diffs[0].NewPath != "b.go" || !reflect.DeepEqual(diffs[0].Changed, []int{1}) {
		t.Fatalf("rename = %+v", diffs)
	}
}

func TestParseUnifiedDiffBadHunk(t *testing.T) {
	if _, err := ParseUnifiedDiff("diff --git a/a b/a\n--- a/a\n+++ b/a\n@@ -x +1 @@\n"); err == nil {
		t.Fatal("bad hunk header accepted")
	}
}

func TestGoldFiles(t *testing.T) {
	diffs, _ := ParseUnifiedDiff(fixturePatch)
	gold, added := GoldFiles(diffs, GoldOptions{})
	if want := []string{"img.png", "internal/x/x.go", "old.txt", "pkg/mod.py"}; !reflect.DeepEqual(gold, want) {
		t.Errorf("gold = %v, want %v", gold, want)
	}
	if !reflect.DeepEqual(added, []string{"pkg/new.py"}) {
		t.Errorf("added = %v", added)
	}
	gold, _ = GoldFiles(diffs, GoldOptions{IncludeTests: true})
	if len(gold) != 5 {
		t.Errorf("with tests: %v", gold)
	}
}

func TestGoldFunctions(t *testing.T) {
	diffs, _ := ParseUnifiedDiff(fixturePatch)
	src := map[string]string{"internal/x/x.go": goBase, "pkg/mod.py": pyBase, "internal/x/x_test.go": "package x\n\nfunc TestX() {}\n"}
	read := func(p string) ([]byte, bool) {
		s, ok := src[p]
		return []byte(s), ok
	}
	got := GoldFunctions(diffs, GoldOptions{}, read)
	// The appended New() after Bye's closing brace and the module-level
	// import are outside every symbol; test files are excluded.
	want := []string{"internal/x/x.go::Greeter.Hello", "pkg/mod.py::Config.load"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("GoldFunctions = %v, want %v", got, want)
	}
	got = GoldFunctions(diffs, GoldOptions{IncludeTests: true}, read)
	if !reflect.DeepEqual(got, append(want[:1:1], "internal/x/x_test.go::TestX", want[1])) {
		t.Fatalf("with tests = %v", got)
	}
}

func TestIsTestPath(t *testing.T) {
	for p, want := range map[string]bool{
		"a/b_test.go": true, "a/testdata/x.go": true, "tests/test_x.py": true,
		"pkg/test_util.py": true, "pkg/util_test.py": true, "conftest.py": true,
		"django/test/testcases.py": false, "a/b.go": false, "pkg/testing.py": false,
	} {
		if got := IsTestPath(p); got != want {
			t.Errorf("IsTestPath(%q) = %v, want %v", p, got, want)
		}
	}
}
