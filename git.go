package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"

	"golang.org/x/mod/modfile"
)

// changes describes what changed between the merge-base and the target
// (working tree or head commit), as input for the affected-main analysis.
type changes struct {
	files   []string // paths of changed source files
	modules []string // module paths whose requirement or checksum changed
	all     bool     // every package must be treated as changed

	// others holds changes to the go.mod / go.sum of modules other than the
	// one being analyzed (e.g. sibling modules in a monorepo). Whether they
	// matter depends on whether that module takes part in the build — as a
	// workspace member or a local replacement — which is only known once
	// the packages are loaded, so affectedMainDirs decides.
	others []otherModChange
}

// otherModChange is a change to the module files of a module other than
// the one being analyzed.
type otherModChange struct {
	goMod   string   // absolute path of the module's go.mod
	modules []string // module paths affected if the module is in the build
	all     bool     // every package is affected if the module is in the build
}

// repo is the git repository being analyzed and the two states under
// comparison: the merge-base commit and the target, which is either the
// head commit or, when head is empty, the working tree.
type repo struct {
	root      string // module root (-C); git commands run here
	top       string // repository top-level directory
	mergeBase string // commit the target is compared against
	head      string // target commit; empty means the working tree
}

// gitChanges collects the changes since the merge-base of base and head.
// With an empty head it compares against the working tree, including
// uncommitted and untracked files; with a non-empty head it compares the
// two commits only, so the result is deterministic. When ignoreComments is
// true, .go files whose changes are limited to comments or formatting are
// excluded. Changes to go.mod, go.sum, and go.work are translated into the
// module paths they actually affect.
func gitChanges(root, base, head string, ignoreComments bool) (changes, error) {
	r, err := openRepo(root, base, head)
	if err != nil {
		return changes{}, err
	}
	return r.changes(ignoreComments)
}

func openRepo(root, base, head string) (repo, error) {
	top, err := gitLines(root, "rev-parse", "--show-toplevel")
	if err != nil {
		return repo{}, fmt.Errorf("%s is not inside a git repository: %v", root, err)
	}
	if len(top) == 0 {
		return repo{}, fmt.Errorf("%s is not inside a git repository", root)
	}
	target := head
	if target == "" {
		target = "HEAD"
	}
	mergeBase, err := gitLines(root, "merge-base", base, target)
	if err != nil {
		return repo{}, fmt.Errorf("cannot resolve merge-base of %s and %s: %v", base, target, err)
	}
	if len(mergeBase) == 0 {
		return repo{}, fmt.Errorf("cannot resolve merge-base of %s and %s", base, target)
	}
	return repo{root: root, top: top[0], mergeBase: mergeBase[0], head: head}, nil
}

func (r repo) changes(ignoreComments bool) (changes, error) {
	var ch changes
	names, err := r.changedNames()
	if err != nil {
		return ch, err
	}
	rootRel, rootErr := r.moduleRootRel()
	// ownModFile reports whether repo-relative f is the go.mod / go.sum of
	// the module being analyzed. If the root cannot be located, every
	// module file is treated as its own, which errs on the safe side.
	ownModFile := func(f string) bool {
		return rootErr != nil || path.Dir(f) == rootRel
	}
	// governsRoot reports whether a go.work at repo-relative f can apply
	// to the module being analyzed: it must sit in the module root or one
	// of its ancestors.
	governsRoot := func(f string) bool {
		d := path.Dir(f)
		return rootErr != nil || d == "." || d == rootRel || strings.HasPrefix(rootRel, d+"/")
	}
	for _, f := range names {
		if r.goToolIgnored(f) {
			continue // e.g. a go.mod fixture under testdata
		}
		switch filepath.Base(f) {
		case "go.mod":
			if ownModFile(f) {
				if err := r.goModChanges(f, &ch); err != nil {
					return ch, err
				}
				continue
			}
			o, ok, err := r.otherGoModChanges(f)
			if err != nil {
				return ch, err
			}
			if ok {
				ch.others = append(ch.others, o)
			}
		case "go.sum":
			if ownModFile(f) {
				r.goSumChanges(f, true, &ch)
				continue
			}
			var sub changes
			r.goSumChanges(f, true, &sub)
			if sub.all || len(sub.modules) > 0 {
				ch.others = append(ch.others, otherModChange{
					goMod:   r.abs(strings.TrimSuffix(f, "go.sum") + "go.mod"),
					modules: sub.modules,
					all:     sub.all,
				})
			}
		case "go.work":
			if !governsRoot(f) {
				continue // a workspace the analyzed module is not part of
			}
			mods, all, err := r.goWorkChanges(f)
			if err != nil {
				return ch, err
			}
			ch.all = ch.all || all
			ch.modules = append(ch.modules, mods...)
		case "go.work.sum":
			if !governsRoot(f) {
				continue
			}
			r.goSumChanges(f, false, &ch)
		default:
			if ignoreComments && strings.HasSuffix(f, ".go") && r.commentOnly(f) {
				continue
			}
			ch.files = append(ch.files, r.abs(f))
		}
	}
	return ch, nil
}

// changedNames returns the repo-relative paths of files that differ
// between the merge-base and the target state. --no-renames lists a
// renamed file as a deletion plus an addition, so the package that lost
// the file is analyzed too.
func (r repo) changedNames() ([]string, error) {
	if r.head != "" {
		return gitPaths(r.root, "diff", "--name-only", "--no-renames", "-z", r.mergeBase, r.head)
	}
	names, err := gitPaths(r.root, "diff", "--name-only", "--no-renames", "-z", r.mergeBase)
	if err != nil {
		return nil, err
	}
	untracked, err := gitPaths(r.root, "ls-files", "--others", "--exclude-standard", "--full-name", "-z")
	if err != nil {
		return nil, err
	}
	return append(names, untracked...), nil
}

// goToolIgnored reports whether the go tool would never include the
// repo-relative file f in the build of the module being analyzed, because
// a directory on its path below the module root is named "testdata" or
// starts with "." or "_". Module files (go.mod, go.sum, ...) under such
// directories are fixtures, not part of the build.
func (r repo) goToolIgnored(f string) bool {
	abs, err := filepath.Abs(r.root)
	if err != nil {
		return false
	}
	return goToolIgnores(r.abs(f), canonicalize(abs))
}

// goModChanges folds a change to the go.mod at repo-relative path f into ch.
func (r repo) goModChanges(f string, ch *changes) error {
	old, _ := r.oldContent(f) // empty for files new since the merge-base
	cur, err := r.newContent(f)
	if err != nil {
		ch.all = true // deleted; cannot tell what it affected
		return nil
	}
	mods, all, err := diffGoMod(old, cur)
	if err != nil {
		return fmt.Errorf("%s: %w", f, err)
	}
	ch.all = ch.all || all
	ch.modules = append(ch.modules, mods...)
	return nil
}

// otherGoModChanges translates a change to the go.mod at repo-relative
// path f, belonging to a module other than the one being analyzed, into
// the modules it affects should that module take part in the build. Only
// the main module's go / toolchain / godebug settings apply to a build, so
// changes that would affect everything for the main module affect just
// that module's own packages here (its language version may change). ok
// is false when the go.mod was deleted: a module that no longer exists
// cannot be in the build, and dropping it from the workspace or a replace
// shows up as a change to those files instead.
func (r repo) otherGoModChanges(f string) (o otherModChange, ok bool, err error) {
	old, _ := r.oldContent(f) // empty for files new since the merge-base
	cur, err := r.newContent(f)
	if err != nil {
		return o, false, nil
	}
	mods, all, err := diffGoMod(old, cur)
	if err != nil {
		return o, false, fmt.Errorf("%s: %w", f, err)
	}
	if all {
		for _, b := range [][]byte{old, cur} {
			if p, err := parseModulePath(b); err == nil {
				mods = append(mods, p)
			}
		}
	}
	if len(mods) == 0 {
		return o, false, nil
	}
	return otherModChange{goMod: r.abs(f), modules: mods}, true, nil
}

// goSumChanges folds a change to the go.sum or go.work.sum at
// repo-relative path f into ch. Added or removed hashes cannot change the
// build, a changed hash for an existing version can. For go.sum this
// reasoning requires the sibling go.mod to have module graph pruning
// (go >= 1.17), which checkPruned selects.
func (r repo) goSumChanges(f string, checkPruned bool, ch *changes) {
	if checkPruned {
		gomod, err := r.newContent(strings.TrimSuffix(f, "go.sum") + "go.mod")
		if err != nil || !modGraphPruned(gomod) {
			ch.all = true // pre-1.17 module or missing go.mod
			return
		}
	}
	old, _ := r.oldContent(f)
	cur, err := r.newContent(f)
	if err != nil {
		ch.all = true // deleted; cannot tell what it affected
		return
	}
	ch.modules = append(ch.modules, diffGoSum(old, cur)...)
}

// goWorkChanges translates a change to the go.work file at repo-relative
// path f into the module paths it affects.
func (r repo) goWorkChanges(f string) ([]string, bool, error) {
	old, errOld := r.oldContent(f)
	cur, errCur := r.newContent(f)
	switch {
	case errOld != nil && errCur != nil:
		return nil, true, nil
	case errOld != nil:
		// go.work added: every member joins the workspace at once.
		return r.workMembersImpact(f, cur, false)
	case errCur != nil:
		// go.work removed: every member leaves the workspace.
		return r.workMembersImpact(f, old, true)
	}

	d, err := diffGoWork(old, cur)
	if err != nil {
		return nil, false, fmt.Errorf("%s: %w", f, err)
	}
	if d.all {
		return nil, true, nil
	}
	mods := d.modules
	for _, u := range d.removedUses {
		m, all, err := r.useImpact(f, u, true)
		if err != nil || all {
			return nil, true, err
		}
		mods = append(mods, m...)
	}
	for _, u := range d.addedUses {
		m, all, err := r.useImpact(f, u, false)
		if err != nil || all {
			return nil, true, err
		}
		mods = append(mods, m...)
	}
	return mods, false, nil
}

// workMembersImpact handles an added or removed go.work, whose members all
// join or leave the workspace at once. atOld selects whether member go.mod
// files are read at the merge-base (removed) or at the target state
// (added).
func (r repo) workMembersImpact(f string, work []byte, atOld bool) ([]string, bool, error) {
	w, err := modfile.ParseWork("go.work", work, nil)
	if err != nil {
		return nil, false, fmt.Errorf("%s: %w", f, err)
	}
	if w.Toolchain != nil || len(w.Godebug) > 0 {
		// An explicit toolchain switch or workspace godebug settings
		// appear or disappear with the file.
		return nil, true, nil
	}
	var mods []string
	for k := range replaceEntries(w.Replace) {
		mods = append(mods, strings.SplitN(k, "@", 2)[0])
	}
	for u := range useSet(w) {
		m, all, err := r.useImpact(f, u, atOld)
		if err != nil || all {
			return nil, true, err
		}
		mods = append(mods, m...)
	}
	return mods, false, nil
}

// useImpact computes the impact of the module at use directory u (relative
// to the go.work at repo-relative path f) joining or leaving the
// workspace. The module being analyzed (-C) is skipped: it is built from
// its local sources either way and its own requirements already apply.
func (r repo) useImpact(f, u string, atOld bool) ([]string, bool, error) {
	memberRel := path.Join(path.Dir(f), u, "go.mod")
	member, err := r.content(memberRel, atOld)
	if err != nil {
		return nil, true, nil // e.g. a use directory outside the repository; be safe
	}
	rootRel, err := r.moduleRootRel()
	if err != nil {
		return nil, true, nil
	}
	base, err := r.content(path.Join(rootRel, "go.mod"), atOld)
	if err != nil {
		return nil, true, nil
	}

	memberPath, err := parseModulePath(member)
	if err != nil {
		return nil, false, fmt.Errorf("%s: %w", memberRel, err)
	}
	basePath, err := parseModulePath(base)
	if err != nil {
		return nil, false, fmt.Errorf("go.mod: %w", err)
	}
	if memberPath == basePath {
		return nil, false, nil
	}
	mods, all, err := memberImpact(member, base)
	if err != nil {
		return nil, false, fmt.Errorf("%s: %w", memberRel, err)
	}
	return mods, all, nil
}

// commentOnly reports whether the .go file at repo-relative path rel
// differs between the merge-base and the target state only in comments or
// formatting, in which case the change cannot affect any build. Deleted
// and newly added files count as real changes.
func (r repo) commentOnly(rel string) bool {
	cur, err := r.newContent(rel)
	if err != nil {
		return false
	}
	old, err := r.oldContent(rel)
	if err != nil {
		return false
	}
	return sameGoTokens(old, cur)
}

// oldContent returns the file's content at the merge-base commit.
func (r repo) oldContent(rel string) ([]byte, error) {
	return gitOutput(r.root, "show", r.mergeBase+":"+rel)
}

// newContent returns the file's content at the target state: the head
// commit, or the working tree when head is empty.
func (r repo) newContent(rel string) ([]byte, error) {
	if r.head == "" {
		return os.ReadFile(r.abs(rel))
	}
	return gitOutput(r.root, "show", r.head+":"+rel)
}

// content returns the file's content at the merge-base or target state.
func (r repo) content(rel string, atOld bool) ([]byte, error) {
	if atOld {
		return r.oldContent(rel)
	}
	return r.newContent(rel)
}

// abs returns the working-tree path of a repo-relative file.
func (r repo) abs(rel string) string {
	return filepath.Join(r.top, filepath.FromSlash(rel))
}

// moduleRootRel returns the repo-relative, slash-separated path of the
// module root (-C).
func (r repo) moduleRootRel() (string, error) {
	abs, err := filepath.Abs(r.root)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(r.top, canonicalize(abs))
	if err != nil || strings.HasPrefix(rel, "..") {
		return "", fmt.Errorf("%s is outside the repository %s", r.root, r.top)
	}
	return filepath.ToSlash(rel), nil
}

// defaultBase returns the repository's default branch, i.e. the branch
// that origin/HEAD points at.
func defaultBase(root string) (string, error) {
	ref, err := gitLines(root, "symbolic-ref", "--short", "refs/remotes/origin/HEAD")
	if err != nil || len(ref) == 0 {
		return "", fmt.Errorf("cannot detect the default branch because origin/HEAD is not set; specify -base explicitly (or run: git remote set-head origin --auto)")
	}
	return ref[0], nil
}

// gitOutput runs git with the given arguments in dir and returns its stdout.
func gitOutput(dir string, args ...string) ([]byte, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok && len(ee.Stderr) > 0 {
			return nil, fmt.Errorf("git %s: %s", strings.Join(args, " "), bytes.TrimSpace(ee.Stderr))
		}
		return nil, fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return out, nil
}

// gitPaths runs git with the given arguments (which must include -z) in
// dir and returns its output as a slice of NUL-separated paths. Unlike
// line-based output, -z output is never quoted, so paths containing
// non-ASCII or special characters come back verbatim.
func gitPaths(dir string, args ...string) ([]string, error) {
	out, err := gitOutput(dir, args...)
	if err != nil {
		return nil, err
	}
	var paths []string
	for _, p := range strings.Split(string(out), "\x00") {
		if p != "" {
			paths = append(paths, p)
		}
	}
	return paths, nil
}

// gitLines runs git with the given arguments in dir and returns its output
// as a slice of non-empty lines.
func gitLines(dir string, args ...string) ([]string, error) {
	out, err := gitOutput(dir, args...)
	if err != nil {
		return nil, err
	}
	var lines []string
	for _, l := range strings.Split(string(out), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			lines = append(lines, l)
		}
	}
	return lines, nil
}
