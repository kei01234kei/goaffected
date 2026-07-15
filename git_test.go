package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
)

// setupRepo copies the fixture module into a temp dir and turns it into a
// git repository with a single commit.
func setupRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	if err := os.CopyFS(dir, os.DirFS(filepath.Join("testdata", "proj"))); err != nil {
		t.Fatal(err)
	}
	git(t, dir, "init", "-q", "-b", "main")
	commitAll(t, dir, "init")
	return dir
}

// commitAll stages everything and commits.
func commitAll(t *testing.T, dir, msg string) {
	t.Helper()
	git(t, dir, "add", "-A")
	git(t, dir, "-c", "user.name=test", "-c", "user.email=test@example.com", "commit", "-q", "-m", msg)
}

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func write(t *testing.T, dir, rel, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, rel), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// affected runs gitChanges and affectedMainDirs and returns the result.
func affectedIn(t *testing.T, dir, base, head string, ignoreComments bool) []string {
	t.Helper()
	ch, err := gitChanges(dir, base, head, ignoreComments)
	if err != nil {
		t.Fatal(err)
	}
	got, err := affectedMainDirs(dir, ch)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func TestDefaultBase(t *testing.T) {
	dir := setupRepo(t)

	// Without origin/HEAD the default base cannot be detected.
	if got, err := defaultBase(dir); err == nil {
		t.Errorf("defaultBase(repo without origin) = %q, want error", got)
	}

	// In a clone, origin/HEAD points at the remote's default branch.
	clone := filepath.Join(t.TempDir(), "clone")
	cmd := exec.Command("git", "clone", "-q", dir, clone)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git clone: %v\n%s", err, out)
	}
	got, err := defaultBase(clone)
	if err != nil {
		t.Fatal(err)
	}
	if got != "origin/main" {
		t.Errorf("defaultBase(clone) = %q, want %q", got, "origin/main")
	}
}

func TestGitChanges(t *testing.T) {
	dir := setupRepo(t)

	// Unstaged modification to the shared package, plus an untracked file
	// in one of the main packages.
	write(t, dir, "pkg/util/util.go", "package util\n\nfunc Answer() int { return 43 }\n")
	write(t, dir, "cmd/b/extra.go", "package main\n\nvar extra = true\n")

	got := affectedIn(t, dir, "HEAD", "", true)
	if want := []string{"cmd/a", "cmd/b"}; !slices.Equal(got, want) {
		t.Errorf("affected = %v, want %v", got, want)
	}
}

func TestGitChangesBetweenCommits(t *testing.T) {
	dir := setupRepo(t)

	// Commit a change to the shared package on a feature branch.
	git(t, dir, "switch", "-qc", "feature")
	write(t, dir, "pkg/util/util.go", "package util\n\nfunc Answer() int { return 43 }\n")
	commitAll(t, dir, "change util")

	// Dirty the working tree: these must NOT influence the result.
	write(t, dir, "cmd/b/main.go", "package main\n\nfunc main() { println(\"dirty\") }\n")
	write(t, dir, "cmd/b/extra.go", "package main\n\nvar extra = true\n")

	got := affectedIn(t, dir, "main", "feature", true)
	if want := []string{"cmd/a"}; !slices.Equal(got, want) {
		t.Errorf("affected = %v, want %v", got, want)
	}

	// Identical commits produce no changes.
	ch, err := gitChanges(dir, "feature", "feature", true)
	if err != nil {
		t.Fatal(err)
	}
	if len(ch.files) != 0 || len(ch.modules) != 0 || ch.all {
		t.Errorf("gitChanges(feature..feature) = %+v, want empty", ch)
	}
}

func TestCommentOnlyChangeIgnored(t *testing.T) {
	dir := setupRepo(t)

	// Comment and formatting changes only: no main package is affected.
	write(t, dir, "pkg/util/util.go",
		"package util\n\n// Answer returns the answer.\nfunc Answer() int {\n\treturn 42\n}\n")
	ch, err := gitChanges(dir, "HEAD", "", true)
	if err != nil {
		t.Fatal(err)
	}
	if len(ch.files) != 0 || len(ch.modules) != 0 || ch.all {
		t.Errorf("gitChanges = %+v, want empty", ch)
	}

	// With ignoreComments disabled the same change counts as a real one.
	got := affectedIn(t, dir, "HEAD", "", false)
	if want := []string{"cmd/a"}; !slices.Equal(got, want) {
		t.Errorf("with ignoreComments=false: affected = %v, want %v", got, want)
	}

	// A real change in another file must still be detected.
	write(t, dir, "cmd/b/main.go", "package main\n\nfunc main() { println(\"B\") }\n")
	got = affectedIn(t, dir, "HEAD", "", true)
	if want := []string{"cmd/b"}; !slices.Equal(got, want) {
		t.Errorf("affected = %v, want %v", got, want)
	}
}

func TestNonASCIIFilenames(t *testing.T) {
	dir := setupRepo(t)

	// git quotes non-ASCII paths in line-based output (core.quotepath);
	// the NUL-separated listing must return them verbatim, for both a
	// tracked modification and an untracked file.
	write(t, dir, "pkg/util/日本語.go", "package util\n\nfunc Nihongo() int { return 1 }\n")
	commitAll(t, dir, "add non-ascii file")
	write(t, dir, "pkg/util/日本語.go", "package util\n\nfunc Nihongo() int { return 2 }\n")
	write(t, dir, "cmd/b/追加.go", "package main\n\nvar extra = true\n")

	got := affectedIn(t, dir, "HEAD", "", true)
	if want := []string{"cmd/a", "cmd/b"}; !slices.Equal(got, want) {
		t.Errorf("affected = %v, want %v", got, want)
	}
}

func TestRenameAcrossPackages(t *testing.T) {
	dir := setupRepo(t)

	// Commit a second file in pkg/util, then move it verbatim to a new
	// directory. pkg/util loses the file, so cmd/a must be affected even
	// though git detects the move as a rename and no path in pkg/util
	// appears as modified.
	write(t, dir, "pkg/util/extra.go", "package util\n\nfunc Extra() int { return 7 }\n")
	commitAll(t, dir, "add extra")
	if err := os.MkdirAll(filepath.Join(dir, "pkg", "other"), 0o755); err != nil {
		t.Fatal(err)
	}
	git(t, dir, "mv", "pkg/util/extra.go", "pkg/other/extra.go")

	got := affectedIn(t, dir, "HEAD", "", true)
	if want := []string{"cmd/a"}; !slices.Equal(got, want) {
		t.Errorf("affected after rename = %v, want %v", got, want)
	}

	// The committed rename between two commits behaves the same.
	commitAll(t, dir, "move extra")
	got = affectedIn(t, dir, "HEAD~1", "HEAD", true)
	if want := []string{"cmd/a"}; !slices.Equal(got, want) {
		t.Errorf("affected between commits = %v, want %v", got, want)
	}
}

func TestModuleFilesInIgnoredDirsAreIgnored(t *testing.T) {
	dir := setupRepo(t)

	// A go.mod fixture under testdata is not part of the build, so adding
	// one must not affect anything (a new go.mod would otherwise be treated
	// as a module-path change affecting every main).
	if err := os.MkdirAll(filepath.Join(dir, "testdata", "fixture"), 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, dir, "testdata/fixture/go.mod", "module example.com/fixture\n\ngo 1.21\n")

	got := affectedIn(t, dir, "HEAD", "", true)
	if len(got) != 0 {
		t.Errorf("affected = %v, want none", got)
	}
}

func TestGoModChange(t *testing.T) {
	dir := setupRepo(t)

	// Bumping the version of example.com/dep affects only cmd/c, which is
	// the only main importing it. (The replace directive still resolves
	// the module locally, so the tree keeps building.)
	write(t, dir, "go.mod",
		"module example.com/proj\n\ngo 1.21\n\nrequire example.com/dep v0.0.1\n\nreplace example.com/dep => ./dep\n")
	got := affectedIn(t, dir, "HEAD", "", true)
	if want := []string{"cmd/c"}; !slices.Equal(got, want) {
		t.Errorf("affected = %v, want %v", got, want)
	}

	// Changing the go directive affects every main package.
	write(t, dir, "go.mod",
		"module example.com/proj\n\ngo 1.22\n\nrequire example.com/dep v0.0.0\n\nreplace example.com/dep => ./dep\n")
	got = affectedIn(t, dir, "HEAD", "", true)
	if want := []string{"cmd/a", "cmd/b", "cmd/c"}; !slices.Equal(got, want) {
		t.Errorf("affected = %v, want %v", got, want)
	}

	// An unparsable go.mod is reported as an error.
	write(t, dir, "go.mod", "module\n")
	if _, err := gitChanges(dir, "HEAD", "", true); err == nil {
		t.Error("gitChanges with broken go.mod: got nil error, want error")
	}
}

func TestGoWorkChange(t *testing.T) {
	dir := setupRepo(t)

	// Commit a workspace with an extra module that nothing imports.
	write(t, dir, "go.work", "go 1.21\n\nuse .\n")
	if err := os.MkdirAll(filepath.Join(dir, "extra"), 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, dir, "extra/go.mod", "module example.com/extra\n\ngo 1.21\n")
	write(t, dir, "extra/extra.go", "package extra\n")
	commitAll(t, dir, "add workspace")

	// Adding a use directive only affects mains that import that module —
	// here none, so nothing is affected.
	write(t, dir, "go.work", "go 1.21\n\nuse (\n\t.\n\t./extra\n)\n")
	got := affectedIn(t, dir, "HEAD", "", true)
	if len(got) != 0 {
		t.Errorf("affected after use added = %v, want none", got)
	}

	// Changing the go directive affects every main package.
	write(t, dir, "go.work", "go 1.22\n\nuse .\n")
	got = affectedIn(t, dir, "HEAD", "", true)
	if want := []string{"cmd/a", "cmd/b", "cmd/c"}; !slices.Equal(got, want) {
		t.Errorf("affected after go directive change = %v, want %v", got, want)
	}
}

func TestGoWorkAddRemove(t *testing.T) {
	dir := setupRepo(t)

	// Commit an extra workspace-candidate module that requires a higher
	// version of example.com/dep than the root module does.
	if err := os.MkdirAll(filepath.Join(dir, "extra"), 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, dir, "extra/go.mod", "module example.com/extra\n\ngo 1.21\n\nrequire example.com/dep v0.0.2\n")
	write(t, dir, "extra/extra.go", "package extra\n")
	commitAll(t, dir, "add extra module")

	// Adding a go.work that only uses the module itself changes nothing.
	write(t, dir, "go.work", "go 1.21\n\nuse .\n")
	got := affectedIn(t, dir, "HEAD", "", true)
	if len(got) != 0 {
		t.Errorf("affected after adding go.work with use . = %v, want none", got)
	}

	// Adding extra as a member raises the selected version of
	// example.com/dep, so the main importing it is affected — but only
	// that one.
	write(t, dir, "go.work", "go 1.21\n\nuse (\n\t.\n\t./extra\n)\n")
	got = affectedIn(t, dir, "HEAD", "", true)
	if want := []string{"cmd/c"}; !slices.Equal(got, want) {
		t.Errorf("affected after adding member = %v, want %v", got, want)
	}

	// Removing a committed go.work is symmetric.
	commitAll(t, dir, "add go.work")
	if err := os.Remove(filepath.Join(dir, "go.work")); err != nil {
		t.Fatal(err)
	}
	got = affectedIn(t, dir, "HEAD", "", true)
	if want := []string{"cmd/c"}; !slices.Equal(got, want) {
		t.Errorf("affected after removing go.work = %v, want %v", got, want)
	}
}

func TestGoSumChanges(t *testing.T) {
	dir := setupRepo(t)

	// Commit a go.sum so there is an old version to diff against.
	write(t, dir, "go.sum", "example.com/dep v0.0.0 h1:AAA=\nexample.com/dep v0.0.0/go.mod h1:AAB=\n")
	commitAll(t, dir, "add go.sum")

	// Added or removed entries (e.g. pruned by go mod tidy) cannot change
	// the build and are ignored.
	write(t, dir, "go.sum",
		"example.com/dep v0.0.0 h1:AAA=\nexample.com/dep v0.0.0/go.mod h1:AAB=\nexample.com/x v1.0.0 h1:XXX=\n")
	ch, err := gitChanges(dir, "HEAD", "", true)
	if err != nil {
		t.Fatal(err)
	}
	if len(ch.files) != 0 || len(ch.modules) != 0 || ch.all {
		t.Errorf("gitChanges(entries added) = %+v, want empty", ch)
	}

	// A changed hash for the same version means the content behind it
	// changed: mains importing that module are affected.
	write(t, dir, "go.sum", "example.com/dep v0.0.0 h1:MOVED=\nexample.com/dep v0.0.0/go.mod h1:AAB=\n")
	got := affectedIn(t, dir, "HEAD", "", true)
	if want := []string{"cmd/c"}; !slices.Equal(got, want) {
		t.Errorf("affected = %v, want %v", got, want)
	}
}
