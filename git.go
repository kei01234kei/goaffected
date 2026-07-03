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
}

// gitChanges collects the changes since the merge-base of base and head.
// With an empty head it compares against the working tree, including
// uncommitted and untracked files; with a non-empty head it compares the
// two commits only, so the result is deterministic. When ignoreComments is
// true, .go files whose changes are limited to comments or formatting are
// excluded. go.mod and go.sum changes are translated into the module paths
// whose requirements actually changed.
func gitChanges(root, base, head string, ignoreComments bool) (changes, error) {
	var ch changes
	top, err := gitLines(root, "rev-parse", "--show-toplevel")
	if err != nil || len(top) == 0 {
		return ch, fmt.Errorf("%s is not inside a git repository: %v", root, err)
	}
	target := head
	if target == "" {
		target = "HEAD"
	}
	mergeBase, err := gitLines(root, "merge-base", base, target)
	if err != nil || len(mergeBase) == 0 {
		return ch, fmt.Errorf("cannot resolve merge-base of %s and %s: %v", base, target, err)
	}

	var names []string
	if head == "" {
		names, err = gitLines(root, "diff", "--name-only", mergeBase[0])
		if err != nil {
			return ch, err
		}
		untracked, err := gitLines(root, "ls-files", "--others", "--exclude-standard", "--full-name")
		if err != nil {
			return ch, err
		}
		names = append(names, untracked...)
	} else {
		names, err = gitLines(root, "diff", "--name-only", mergeBase[0], head)
		if err != nil {
			return ch, err
		}
	}

	for _, f := range names {
		abs := filepath.Join(top[0], filepath.FromSlash(f))
		switch filepath.Base(f) {
		case "go.work":
			mods, all, err := goWorkChanges(root, top[0], mergeBase[0], head, f, abs)
			if err != nil {
				return ch, err
			}
			ch.all = ch.all || all
			ch.modules = append(ch.modules, mods...)
			continue
		case "go.work.sum":
			// Same reasoning as go.sum: added or removed hashes
			// cannot change the build, a changed hash for an
			// existing version can.
			old, _ := gitOutput(root, "show", mergeBase[0]+":"+f)
			cur, err := targetContent(root, head, f, abs)
			if err != nil {
				ch.all = true
				continue
			}
			ch.modules = append(ch.modules, diffGoSum(old, cur)...)
			continue
		case "go.mod":
			// Old content is empty for files new since the merge-base.
			old, _ := gitOutput(root, "show", mergeBase[0]+":"+f)
			cur, err := targetContent(root, head, f, abs)
			if err != nil {
				ch.all = true // deleted; cannot tell what it affected
				continue
			}
			mods, all, err := diffGoMod(old, cur)
			if err != nil {
				return ch, fmt.Errorf("%s: %w", f, err)
			}
			ch.all = ch.all || all
			ch.modules = append(ch.modules, mods...)
			continue
		case "go.sum":
			// With module graph pruning (go >= 1.17), a change to a
			// selected module version also appears in go.mod, so
			// added or removed go.sum entries (e.g. hashes pruned by
			// go mod tidy) cannot change the build. A changed hash
			// for an existing module@version, however, means that
			// version's content itself changed.
			modRel := strings.TrimSuffix(f, "go.sum") + "go.mod"
			gomod, err := targetContent(root, head, modRel, filepath.Join(top[0], filepath.FromSlash(modRel)))
			if err != nil || !modGraphPruned(gomod) {
				ch.all = true // pre-1.17 module or missing go.mod
				continue
			}
			old, _ := gitOutput(root, "show", mergeBase[0]+":"+f)
			cur, err := targetContent(root, head, f, abs)
			if err != nil {
				ch.all = true // deleted; cannot tell what it affected
				continue
			}
			ch.modules = append(ch.modules, diffGoSum(old, cur)...)
			continue
		}
		if ignoreComments && strings.HasSuffix(f, ".go") && commentOnlyChange(root, mergeBase[0], head, f, abs) {
			continue
		}
		ch.files = append(ch.files, abs)
	}
	return ch, nil
}

// goWorkChanges translates a change to the go.work file at repo-relative
// path f into the module paths it affects.
func goWorkChanges(root, top, mergeBase, head, f, abs string) ([]string, bool, error) {
	old, errOld := gitOutput(root, "show", mergeBase+":"+f)
	cur, errCur := targetContent(root, head, f, abs)
	switch {
	case errOld != nil && errCur != nil:
		return nil, true, nil
	case errOld != nil:
		// go.work added: every member joins the workspace at once.
		return workMembersImpact(root, top, mergeBase, head, f, cur, false)
	case errCur != nil:
		// go.work removed: every member leaves the workspace.
		return workMembersImpact(root, top, mergeBase, head, f, old, true)
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
		m, all, err := useImpact(root, top, mergeBase, head, f, u, true)
		if err != nil || all {
			return nil, true, err
		}
		mods = append(mods, m...)
	}
	for _, u := range d.addedUses {
		m, all, err := useImpact(root, top, mergeBase, head, f, u, false)
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
func workMembersImpact(root, top, mergeBase, head, f string, work []byte, atOld bool) ([]string, bool, error) {
	w, err := modfile.ParseWork("go.work", work, nil)
	if err != nil {
		return nil, false, fmt.Errorf("%s: %w", f, err)
	}
	if w.Toolchain != nil {
		return nil, true, nil // an explicit toolchain switch appears or disappears
	}
	var mods []string
	for k := range replaceEntries(w.Replace) {
		mods = append(mods, strings.SplitN(k, "@", 2)[0])
	}
	for u := range useSet(w) {
		m, all, err := useImpact(root, top, mergeBase, head, f, u, atOld)
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
func useImpact(root, top, mergeBase, head, f, u string, atOld bool) ([]string, bool, error) {
	read := func(rel string) ([]byte, error) {
		if atOld {
			return gitOutput(root, "show", mergeBase+":"+rel)
		}
		return targetContent(root, head, rel, filepath.Join(top, filepath.FromSlash(rel)))
	}

	memberRel := path.Join(path.Dir(f), u, "go.mod")
	member, err := read(memberRel)
	if err != nil {
		return nil, true, nil // e.g. a use directory outside the repository; be safe
	}

	absRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, false, err
	}
	relRoot, err := filepath.Rel(top, canonicalize(absRoot))
	if err != nil || strings.HasPrefix(relRoot, "..") {
		return nil, true, nil
	}
	base, err := read(path.Join(filepath.ToSlash(relRoot), "go.mod"))
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

// defaultBase returns the repository's default branch, i.e. the branch
// that origin/HEAD points at.
func defaultBase(root string) (string, error) {
	ref, err := gitLines(root, "symbolic-ref", "--short", "refs/remotes/origin/HEAD")
	if err != nil || len(ref) == 0 {
		return "", fmt.Errorf("cannot detect the default branch because origin/HEAD is not set; specify -base explicitly (or run: git remote set-head origin --auto)")
	}
	return ref[0], nil
}

// commentOnlyChange reports whether the .go file (rel is its
// repository-relative path) differs between commit and either the head
// commit or, with an empty head, the working-tree file at abs, only in
// comments or formatting, in which case the change cannot affect any
// build. Deleted files and files that did not exist at commit count as
// real changes.
func commentOnlyChange(root, commit, head, rel, abs string) bool {
	cur, err := targetContent(root, head, rel, abs)
	if err != nil {
		return false
	}
	old, err := gitOutput(root, "show", commit+":"+rel)
	if err != nil {
		return false
	}
	return sameGoTokens(old, cur)
}

// targetContent returns the content of the file being analyzed: the
// working-tree file at abs, or its version at the head commit when head is
// non-empty.
func targetContent(root, head, rel, abs string) ([]byte, error) {
	if head == "" {
		return os.ReadFile(abs)
	}
	return gitOutput(root, "show", head+":"+rel)
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
