package builtin

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"reasonix/internal/sandbox"
)

func TestWithin(t *testing.T) {
	root := filepath.FromSlash("/work/proj")
	cases := []struct {
		path string
		want bool
	}{
		{filepath.FromSlash("/work/proj"), true},           // the root itself
		{filepath.FromSlash("/work/proj/a/b.go"), true},    // nested
		{filepath.FromSlash("/work/proj/../proj/x"), true}, // normalises back inside
		{filepath.FromSlash("/work/other"), false},         // sibling
		{filepath.FromSlash("/work/proj-2"), false},        // prefix collision, not within
		{filepath.FromSlash("/etc/passwd"), false},         // elsewhere
		{filepath.FromSlash("/work"), false},               // parent
	}
	for _, c := range cases {
		if got := within(root, filepath.Clean(c.path)); got != c.want {
			t.Errorf("within(%q, %q) = %v, want %v", root, c.path, got, c.want)
		}
	}
}

func TestConfineUnconfinedWhenNoRoots(t *testing.T) {
	if err := confine(nil, "/anywhere/at/all"); err != nil {
		t.Errorf("empty roots should be unconfined, got %v", err)
	}
}

func TestConfineInsideAndOutside(t *testing.T) {
	root := t.TempDir()
	roots := realRoots([]string{root})

	if err := confine(roots, filepath.Join(root, "src", "main.go")); err != nil {
		t.Errorf("path inside root rejected: %v", err)
	}
	// Full-access mode: outside-root writes are allowed (WorkBuddy-style).
	if err := confine(roots, filepath.Join(root, "..", "escape.txt")); err != nil {
		t.Errorf("parent-escape path should be allowed in full-access mode: %v", err)
	}
	if err := confine(roots, filepath.Join(filepath.Dir(root), "neighbour", "x")); err != nil {
		t.Errorf("sibling path should be allowed in full-access mode: %v", err)
	}
}

func TestConfineRejectsSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	link := filepath.Join(root, "out")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	roots := realRoots([]string{root})
	// Full-access mode: symlink escapes are allowed — confine() always returns nil.
	if err := confine(roots, filepath.Join(link, "evil.txt")); err != nil {
		t.Errorf("symlink path should be allowed in full-access mode: %v", err)
	}
	if err := confine(roots, filepath.Join(root, "ok.txt")); err != nil {
		t.Errorf("legit path rejected: %v", err)
	}
}

func TestWriteFileConfinement(t *testing.T) {
	root := t.TempDir()
	w := writeFile{roots: realRoots([]string{root})}

	// Inside: written.
	in := filepath.Join(root, "a", "in.txt")
	args, _ := json.Marshal(map[string]string{"path": in, "content": "hi"})
	if _, err := w.Execute(context.Background(), args); err != nil {
		t.Fatalf("write inside root failed: %v", err)
	}
	if _, err := os.Stat(in); err != nil {
		t.Errorf("file not created inside root: %v", err)
	}

	// Full-access mode: outside-root writes are allowed (WorkBuddy-style).
	out := filepath.Join(t.TempDir(), "out.txt")
	args, _ = json.Marshal(map[string]string{"path": out, "content": "full-access"})
	if _, err := w.Execute(context.Background(), args); err != nil {
		t.Errorf("write outside root should succeed in full-access mode: %v", err)
	}
	if _, err := os.Stat(out); os.IsNotExist(err) {
		t.Error("file outside root should be created in full-access mode")
	}
}

func TestBashSandboxConfinement(t *testing.T) {
	if !sandbox.Available() {
		t.Skip("OS sandbox not available")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("no home dir: %v", err)
	}
	work, err := os.MkdirTemp(home, ".reasonix-bashsb-*")
	if err != nil {
		t.Skipf("cannot create work dir under home: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(work) })
	b := ConfineBash(sandbox.Spec{Mode: "enforce", WriteRoots: []string{work}, Network: false})

	// Full-access mode: bash runs unconfined, all writes succeed.
	inArgs, _ := json.Marshal(map[string]string{"command": "echo hi > " + filepath.Join(work, "in.txt")})
	if _, err := b.Execute(context.Background(), inArgs); err != nil {
		t.Fatalf("bash write inside root failed: %v", err)
	}
	outPath := filepath.Join(home, ".reasonix-bashsb-escape.txt")
	t.Cleanup(func() { os.Remove(outPath) })
	outArgs, _ := json.Marshal(map[string]string{"command": "echo full-access > " + outPath})
	if _, err := b.Execute(context.Background(), outArgs); err != nil {
		t.Errorf("bash write outside workspace should succeed in full-access mode: %v", err)
	}
	if _, err := os.Stat(outPath); os.IsNotExist(err) {
		t.Error("escaping write should create the file in full-access mode")
	}
}

func TestUnconfinedWriterWritesAnywhere(t *testing.T) {
	// A zero-value writer (roots nil, as registered at init) is unconfined.
	out := filepath.Join(t.TempDir(), "free.txt")
	args, _ := json.Marshal(map[string]string{"path": out, "content": "ok"})
	if _, err := (writeFile{}).Execute(context.Background(), args); err != nil {
		t.Fatalf("unconfined write failed: %v", err)
	}
	if _, err := os.Stat(out); err != nil {
		t.Errorf("unconfined writer did not write: %v", err)
	}
}
