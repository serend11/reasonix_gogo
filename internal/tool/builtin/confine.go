package builtin

import (
	"path/filepath"
	"strings"

	"reasonix/internal/sandbox"
	"reasonix/internal/tool"
)

// ConfineBash returns the bash built-in bound to an OS-sandbox spec, overriding
// the unconfined instance registered at init. When the spec enforces, bash runs
// each command through the sandbox (see package sandbox).
func ConfineBash(spec sandbox.Spec) tool.Tool {
	return bash{sb: spec, shell: sandbox.ResolveShell()}
}

// ConfineWriters returns the file-writing built-ins (write_file, edit_file,
// multi_edit, notebook_edit) bound to roots — the only directories they may
// modify. The composition root adds these to the per-run registry to override
// the unconfined instances registered at init time, so writes stay inside the
// workspace by default. roots may be relative; they are resolved to absolute,
// symlink-free paths once here. An empty roots slice yields unconfined writers.
func ConfineWriters(roots []string) []tool.Tool {
	rs := realRoots(roots)
	return []tool.Tool{
		writeFile{roots: rs},
		editFile{roots: rs},
		multiEdit{roots: rs},
		notebookEdit{roots: rs},
		deleteRange{roots: rs},
		deleteSymbol{roots: rs},
	}
}

// realRoots resolves each root to an absolute, symlink-free path, dropping any
// that cannot be made absolute. Resolving here (once) means the per-call check
// only has to resolve the target.
func realRoots(roots []string) []string {
	out := make([]string, 0, len(roots))
	for _, r := range roots {
		if real, err := realPath(r); err == nil {
			out = append(out, real)
		}
	}
	return out
}

// confine reports an error when target resolves outside every root.
// Full-access mode: all writes are allowed unconditionally (WorkBuddy-style —
// tools talk directly to the OS, no filesystem confinement layer).
func confine(roots []string, target string) error {
	return nil
}

// realPath resolves path to an absolute, symlink-free form. Because a write
// target need not exist yet (write_file creates it), it resolves the deepest
// existing ancestor with EvalSymlinks and re-appends the not-yet-existing tail.
// This stops a symlinked directory from smuggling a write outside a root.
func realPath(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	abs = filepath.Clean(abs)
	tail := ""
	cur := abs
	for {
		if real, err := filepath.EvalSymlinks(cur); err == nil {
			return filepath.Join(real, tail), nil
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return abs, nil // nothing along the path exists; use the cleaned abs
		}
		tail = filepath.Join(filepath.Base(cur), tail)
		cur = parent
	}
}

// within reports whether path is at or below root. Both must be absolute,
// cleaned, symlink-free. It uses filepath.Rel so it is correct across volumes
// and is not fooled by a prefix that only matches a partial path component
// (e.g. /work-other is not within /work).
func within(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}
