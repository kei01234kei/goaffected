package main

import (
	"fmt"
	"log"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"golang.org/x/tools/go/packages"
)

// affectedMainDirs loads the module rooted at root and returns the
// directories (relative to root, slash-separated) of main packages whose
// transitive imports include a package touched by the given changes:
// a changed source file, or membership in a module whose requirement
// changed.
func affectedMainDirs(root string, ch changes) ([]string, error) {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	// Resolve symlinks so paths from git, go/packages, and the command
	// line compare equal (e.g. /var vs /private/var on macOS).
	absRoot = canonicalize(absRoot)

	cfg := &packages.Config{
		Mode: packages.NeedName | packages.NeedFiles | packages.NeedImports |
			packages.NeedDeps | packages.NeedEmbedFiles | packages.NeedModule,
		Dir: absRoot,
	}
	// go/packages passes -mod=mod to go list, which workspace mode
	// rejects; force the readonly default there.
	if goworkActive(absRoot) {
		cfg.BuildFlags = []string{"-mod=readonly"}
	}
	pkgs, err := packages.Load(cfg, "./...")
	if err != nil {
		return nil, err
	}
	if packages.PrintErrors(pkgs) > 0 {
		return nil, fmt.Errorf("could not load packages in %s", absRoot)
	}

	changedIDs := map[string]bool{}
	changedMods := map[string]bool{}
	for _, m := range ch.modules {
		changedMods[m] = true
	}

	// Index every package in the graph (including dependencies) by
	// directory and by source/embedded file, and mark packages that
	// belong to a module whose requirement changed.
	dirPkgs := map[string][]*packages.Package{}
	filePkg := map[string]*packages.Package{}
	packages.Visit(pkgs, nil, func(p *packages.Package) {
		if p.Dir != "" {
			dirPkgs[p.Dir] = append(dirPkgs[p.Dir], p)
		}
		for _, files := range [][]string{p.GoFiles, p.OtherFiles, p.EmbedFiles} {
			for _, f := range files {
				filePkg[f] = p
			}
		}
		if p.Module != nil && changedMods[p.Module.Path] {
			changedIDs[p.ID] = true
		}
	})

	for _, f := range ch.files {
		abs := f
		if !filepath.IsAbs(abs) {
			abs = filepath.Join(absRoot, f)
		}
		abs = canonicalize(filepath.Clean(abs))

		if strings.HasSuffix(abs, "_test.go") {
			continue // test files never affect built binaries
		}
		if p, ok := filePkg[abs]; ok {
			changedIDs[p.ID] = true
			continue
		}
		if strings.HasSuffix(abs, ".go") {
			// The file may have been deleted or excluded by build
			// tags; fall back to the package in its directory.
			if ps, ok := dirPkgs[filepath.Dir(abs)]; ok {
				for _, p := range ps {
					changedIDs[p.ID] = true
				}
				continue
			}
			// Files outside the module root (e.g. sibling modules in a
			// monorepo) are silently skipped; warn only inside the module.
			rel, err := filepath.Rel(absRoot, abs)
			if err == nil && !strings.HasPrefix(rel, "..") && !goToolIgnores(abs, absRoot) {
				log.Printf("warning: %s does not belong to any loaded package; ignoring", f)
			}
		}
	}

	memo := map[string]bool{}
	var affected func(*packages.Package) bool
	affected = func(p *packages.Package) bool {
		if v, ok := memo[p.ID]; ok {
			return v
		}
		memo[p.ID] = false
		v := changedIDs[p.ID]
		if !v {
			for _, imp := range p.Imports {
				if affected(imp) {
					v = true
					break
				}
			}
		}
		memo[p.ID] = v
		return v
	}

	var dirs []string
	for _, p := range pkgs {
		if p.Name != "main" {
			continue
		}
		if !ch.all && !affected(p) {
			continue
		}
		rel, err := filepath.Rel(absRoot, p.Dir)
		if err != nil {
			return nil, err
		}
		dirs = append(dirs, filepath.ToSlash(rel))
	}
	sort.Strings(dirs)
	return dirs, nil
}

// goworkActive reports whether builds in dir run in workspace mode.
func goworkActive(dir string) bool {
	cmd := exec.Command("go", "env", "GOWORK")
	cmd.Dir = dir
	out, err := cmd.Output()
	gowork := strings.TrimSpace(string(out))
	return err == nil && gowork != "" && gowork != "off"
}

// goToolIgnores reports whether the go tool would never include the file in
// any package because a directory on its path below root is named
// "testdata" or starts with "." or "_".
func goToolIgnores(abs, root string) bool {
	rel, err := filepath.Rel(root, filepath.Dir(abs))
	if err != nil {
		return false
	}
	for _, elem := range strings.Split(filepath.ToSlash(rel), "/") {
		if elem == "." || elem == ".." {
			continue
		}
		if elem == "testdata" || strings.HasPrefix(elem, ".") || strings.HasPrefix(elem, "_") {
			return true
		}
	}
	return false
}

// canonicalize resolves symlinks in path. If the file itself does not exist
// (e.g. it was deleted), its parent directory is resolved instead.
func canonicalize(path string) string {
	if p, err := filepath.EvalSymlinks(path); err == nil {
		return p
	}
	if d, err := filepath.EvalSymlinks(filepath.Dir(path)); err == nil {
		return filepath.Join(d, filepath.Base(path))
	}
	return path
}
