package main

import (
	"fmt"
	"maps"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"golang.org/x/mod/modfile"
	"golang.org/x/mod/semver"
)

// diffGoMod compares two go.mod contents and returns the paths of modules
// whose requirement or replacement changed. all is true when the change
// affects every package: the module path, go directive, toolchain, or
// godebug settings changed.
func diffGoMod(old, new []byte) (mods []string, all bool, err error) {
	of, err := modfile.Parse("go.mod", old, nil)
	if err != nil {
		return nil, false, fmt.Errorf("parsing old go.mod: %w", err)
	}
	nf, err := modfile.Parse("go.mod", new, nil)
	if err != nil {
		return nil, false, fmt.Errorf("parsing go.mod: %w", err)
	}
	if modulePath(of) != modulePath(nf) || goDirective(of) != goDirective(nf) ||
		toolchainDirective(of) != toolchainDirective(nf) ||
		!maps.Equal(godebugMap(of.Godebug), godebugMap(nf.Godebug)) {
		return nil, true, nil
	}

	set := map[string]bool{}
	oreq, nreq := requireMap(of), requireMap(nf)
	markChanged(set, oreq, nreq)
	markChanged(set, nreq, oreq)
	orep, nrep := replaceEntries(of.Replace), replaceEntries(nf.Replace)
	markChanged(set, orep, nrep)
	markChanged(set, nrep, orep)

	return slices.Sorted(maps.Keys(set)), false, nil
}

// goWorkDiff describes the difference between two go.work contents.
type goWorkDiff struct {
	all         bool     // go, toolchain, or godebug directive changed
	modules     []string // module paths whose replacement changed
	addedUses   []string // use directories present only in the new file
	removedUses []string // use directories present only in the old file
}

// diffGoWork compares two go.work contents. Use directories are cleaned,
// slash-separated paths relative to the go.work file.
func diffGoWork(old, new []byte) (goWorkDiff, error) {
	var d goWorkDiff
	ow, err := modfile.ParseWork("go.work", old, nil)
	if err != nil {
		return d, fmt.Errorf("parsing old go.work: %w", err)
	}
	nw, err := modfile.ParseWork("go.work", new, nil)
	if err != nil {
		return d, fmt.Errorf("parsing go.work: %w", err)
	}
	if workGo(ow) != workGo(nw) || workToolchain(ow) != workToolchain(nw) ||
		!maps.Equal(godebugMap(ow.Godebug), godebugMap(nw.Godebug)) {
		d.all = true
		return d, nil
	}

	set := map[string]bool{}
	orep, nrep := replaceEntries(ow.Replace), replaceEntries(nw.Replace)
	markChanged(set, orep, nrep)
	markChanged(set, nrep, orep)
	d.modules = slices.Sorted(maps.Keys(set))

	ouse, nuse := useSet(ow), useSet(nw)
	for u := range ouse {
		if !nuse[u] {
			d.removedUses = append(d.removedUses, u)
		}
	}
	for u := range nuse {
		if !ouse[u] {
			d.addedUses = append(d.addedUses, u)
		}
	}
	slices.Sort(d.addedUses)
	slices.Sort(d.removedUses)
	return d, nil
}

// memberImpact returns the module paths whose selected version may change
// when the module described by memberGoMod joins or leaves a workspace
// whose base module is described by baseGoMod: the member itself (its
// packages switch between the local copy and the published version) plus
// every module it requires at a higher version than the base module does
// (minimal version selection over the union of requirements raises the
// selection to the member's version while it is present). Requirements the
// base module already satisfies cannot be changed by the member. all is
// true when the member's go.mod predates module graph pruning (go < 1.17)
// and therefore does not record its full relevant requirements.
func memberImpact(memberGoMod, baseGoMod []byte) (mods []string, all bool, err error) {
	mf, err := modfile.Parse("go.mod", memberGoMod, nil)
	if err != nil {
		return nil, false, fmt.Errorf("parsing member go.mod: %w", err)
	}
	bf, err := modfile.Parse("go.mod", baseGoMod, nil)
	if err != nil {
		return nil, false, fmt.Errorf("parsing base go.mod: %w", err)
	}
	if !prunedGoVersion(goDirective(mf)) {
		return nil, true, nil
	}
	mods = []string{modulePath(mf)}
	base := requireMap(bf)
	for p, v := range requireMap(mf) {
		if bv, ok := base[p]; !ok || semver.Compare(v, bv) > 0 {
			mods = append(mods, p)
		}
	}
	slices.Sort(mods)
	return mods, false, nil
}

// markChanged records the module path of every key in a whose value
// differs in b. Keys are either module paths or path@version pairs.
func markChanged(set map[string]bool, a, b map[string]string) {
	for k, v := range a {
		if b[k] != v {
			set[strings.SplitN(k, "@", 2)[0]] = true
		}
	}
}

// modGraphPruned reports whether the go.mod content declares go 1.17 or

// diffGoSum returns the paths of modules whose recorded hash changed for a
// module@version present in both go.sum contents. Added and removed entries
// are ignored: they appear when hashes are pruned or fetched (e.g. by go
// mod tidy) and cannot change the build, because any change to a selected
// version is recorded in go.mod (see modGraphPruned). A different hash for
// the same version, however, means the content behind that version itself
// changed — possible for private modules that are not verified against the
// checksum database, e.g. when a tag is moved to another commit — and that
// can change the build.
func diffGoSum(old, new []byte) []string {
	om, nm := sumEntries(old), sumEntries(new)
	set := map[string]bool{}
	for key, hash := range om {
		if nhash, ok := nm[key]; ok && nhash != hash {
			set[strings.Fields(key)[0]] = true
		}
	}
	return slices.Sorted(maps.Keys(set))
}

// sumEntries maps "<module> <version>" to its hash for each go.sum line.
func sumEntries(b []byte) map[string]string {
	m := map[string]string{}
	for _, line := range strings.Split(string(b), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 3 {
			m[fields[0]+" "+fields[1]] = fields[2]
		}
	}
	return m
}

// modGraphPruned reports whether the go.mod content declares go 1.17 or
// later, i.e. module graph pruning is in effect. From go 1.17 on, go.mod
// records an explicit require for every module that provides transitively
// imported packages (https://go.dev/ref/mod#graph-pruning), so any change
// to the selected version of a module that contributes code to a binary
// necessarily shows up in go.mod — a go.sum-only diff (e.g. hashes pruned
// by go mod tidy) cannot change the build and can be ignored.
func modGraphPruned(gomod []byte) bool {
	f, err := modfile.Parse("go.mod", gomod, nil)
	return err == nil && prunedGoVersion(goDirective(f))
}

// prunedGoVersion reports whether a go directive value enables module
// graph pruning (go 1.17 or later).
func prunedGoVersion(v string) bool {
	return v != "" && semver.Compare("v"+v, "v1.17") >= 0
}

func modulePath(f *modfile.File) string {
	if f.Module == nil {
		return ""
	}
	return f.Module.Mod.Path
}

func goDirective(f *modfile.File) string {
	if f.Go == nil {
		return ""
	}
	return f.Go.Version
}

func toolchainDirective(f *modfile.File) string {
	if f.Toolchain == nil {
		return ""
	}
	return f.Toolchain.Name
}

func requireMap(f *modfile.File) map[string]string {
	m := map[string]string{}
	for _, r := range f.Require {
		m[r.Mod.Path] = r.Mod.Version
	}
	return m
}

// godebugMap returns godebug settings as a key → value map. Only the main
// module's (or the workspace's) godebug directives apply to a build, and
// they change the runtime defaults compiled into every binary.
func godebugMap(gs []*modfile.Godebug) map[string]string {
	m := map[string]string{}
	for _, g := range gs {
		m[g.Key] = g.Value
	}
	return m
}

func replaceEntries(rs []*modfile.Replace) map[string]string {
	m := map[string]string{}
	for _, r := range rs {
		m[r.Old.Path+"@"+r.Old.Version] = r.New.Path + "@" + r.New.Version
	}
	return m
}

func workGo(f *modfile.WorkFile) string {
	if f.Go == nil {
		return ""
	}
	return f.Go.Version
}

func workToolchain(f *modfile.WorkFile) string {
	if f.Toolchain == nil {
		return ""
	}
	return f.Toolchain.Name
}

func useSet(f *modfile.WorkFile) map[string]bool {
	m := map[string]bool{}
	for _, u := range f.Use {
		m[path.Clean(filepath.ToSlash(u.Path))] = true
	}
	return m
}

// parseModulePath returns the module path declared by a go.mod content.
func parseModulePath(gomod []byte) (string, error) {
	f, err := modfile.Parse("go.mod", gomod, nil)
	if err != nil {
		return "", err
	}
	if f.Module == nil {
		return "", fmt.Errorf("go.mod has no module directive")
	}
	return f.Module.Mod.Path, nil
}
