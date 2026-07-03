# goaffected

A CLI tool that reports which main packages (binaries) are affected by
changed Go-related files (`.go`, `go.mod`, `go.sum`). Use it in CI to decide
which binaries need to be rebuilt or redeployed.

## Installation

```sh
go install github.com/kei01234kei/goaffected@latest
```

## Usage

Prints the directories of the affected main packages, one per line.

```sh
# No arguments: the diff against the default branch (including uncommitted
# changes and untracked files) is taken from git automatically
goaffected
# => cmd/a

# Compare against an explicit ref
goaffected -base origin/develop

# Deterministic mode: consider only the committed diff between two commits
# (uncommitted changes and untracked files in the working tree are ignored)
goaffected -base <base-sha> -head <head-sha>

# Analyze a module in another directory
goaffected -C path/to/module

# Treat comment- and formatting-only changes as regular changes
goaffected -include-comment-and-format
```

When `-base` is omitted, the default branch that `origin/HEAD` points at is
used. If `origin/HEAD` is not set this is an error; pass `-base` explicitly
or set it with `git remote set-head origin --auto`.

With `-head`, only the committed diff between the merge-base of `-base` and
the `-head` commit is considered, so the same two commits always produce
the same result. Note that the import graph is read from the currently
checked-out sources, so in CI run the tool with the `-head` commit checked
out.

The output can be piped straight into `go build` and friends:

```sh
goaffected | sed 's|^|./|' | xargs go build
```

## How it works

See [ARCHITECTURE.md](ARCHITECTURE.md) for the internals: the git
interaction, the import graph construction, and the reachability analysis.

## Rules

- A changed `.go` file → the main packages that transitively import its
  package are affected.
- However, changes limited to comments, blank lines, or formatting are
  ignored because they cannot change the build (disable with
  `-include-comment-and-format`). To stay safe, changes to directive
  comments such as `//go:build` and `//go:embed` count as regular changes,
  and files using cgo (`import "C"`) always count as changed.
- A changed `go.mod` / `go.work` → the old and new contents are compared to
  find the modules whose require / replace / use entries changed; only the
  main packages that transitively import packages of those modules are
  affected. Reordered requires and added or removed `// indirect` comments
  (formatting by `go mod tidy`) produce no diff. Adding or removing go.work
  itself is also analyzed per member (adding a go.work that only contains
  `use .` affects nothing). Changes to the `go` / `toolchain` directives or
  the module path affect every main package. Unparsable files are an error.
- A changed `go.sum` → added and removed entries (e.g. hashes pruned by
  `go mod tidy`) are ignored: from go 1.17 on, the version of every module
  used in the build is recorded in go.mod, so a version-selection change
  always shows up there. However, **a changed hash for the same
  module@version** means the content behind that version itself changed
  (e.g. a moved tag on a private module), so the main packages importing
  that module are affected. (For modules before go 1.17 every main package
  is conservatively treated as affected.)
- A changed `_test.go` file → ignored; it does not affect binaries.
- A changed file embedded via `//go:embed` → treated as a change to the
  embedding package.
- A deleted `.go` file → treated as a change to the package remaining in
  the same directory.
- Any other file (README, etc.) → ignored.
