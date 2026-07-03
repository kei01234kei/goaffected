package main

import (
	"slices"
	"testing"
)

func TestDiffGoMod(t *testing.T) {
	base := `module example.com/proj

go 1.21

require (
	example.com/a v1.0.0
	example.com/b v1.5.0
)

replace example.com/b => ../b
`

	cases := []struct {
		name    string
		old, nu string
		want    []string
		wantAll bool
		wantErr bool
	}{
		{name: "identical", old: base, nu: base},
		{
			name: "version bumped",
			old:  base,
			nu:   "module example.com/proj\n\ngo 1.21\n\nrequire (\n\texample.com/a v1.1.0\n\texample.com/b v1.5.0\n)\n\nreplace example.com/b => ../b\n",
			want: []string{"example.com/a"},
		},
		{
			name: "require added",
			old:  base,
			nu:   base + "\nrequire example.com/c v0.1.0\n",
			want: []string{"example.com/c"},
		},
		{
			name: "require removed",
			old:  base,
			nu:   "module example.com/proj\n\ngo 1.21\n\nrequire example.com/b v1.5.0\n\nreplace example.com/b => ../b\n",
			want: []string{"example.com/a"},
		},
		{
			name: "replace target changed",
			old:  base,
			nu:   "module example.com/proj\n\ngo 1.21\n\nrequire (\n\texample.com/a v1.0.0\n\texample.com/b v1.5.0\n)\n\nreplace example.com/b => ../b2\n",
			want: []string{"example.com/b"},
		},
		{
			name: "reordered only",
			old:  base,
			nu:   "module example.com/proj\n\ngo 1.21\n\nrequire (\n\texample.com/b v1.5.0\n\texample.com/a v1.0.0\n)\n\nreplace example.com/b => ../b\n",
		},
		{
			name:    "go directive changed",
			old:     base,
			nu:      "module example.com/proj\n\ngo 1.22\n\nrequire (\n\texample.com/a v1.0.0\n\texample.com/b v1.5.0\n)\n\nreplace example.com/b => ../b\n",
			wantAll: true,
		},
		{
			name:    "unparsable",
			old:     base,
			nu:      "module\n",
			wantErr: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, all, err := diffGoMod([]byte(tc.old), []byte(tc.nu))
			if (err != nil) != tc.wantErr {
				t.Fatalf("diffGoMod error = %v, wantErr %v", err, tc.wantErr)
			}
			if !slices.Equal(got, tc.want) || all != tc.wantAll {
				t.Errorf("diffGoMod = (%v, %v), want (%v, %v)", got, all, tc.want, tc.wantAll)
			}
		})
	}
}

func TestDiffGoSum(t *testing.T) {
	base := `example.com/a v1.0.0 h1:AAA=
example.com/a v1.0.0/go.mod h1:AAB=
example.com/b v1.5.0 h1:BBB=
example.com/b v1.5.0/go.mod h1:BBC=
`

	cases := []struct {
		name    string
		old, nu string
		want    []string
	}{
		{name: "identical", old: base, nu: base},
		{
			// Hashes added for another version (e.g. go mod download):
			// the selected version is recorded in go.mod, so no change.
			name: "entries added",
			old:  base,
			nu:   base + "example.com/c v0.1.0 h1:CCC=\nexample.com/c v0.1.0/go.mod h1:CCD=\n",
		},
		{
			// Hashes pruned by go mod tidy: no change.
			name: "entries removed",
			old:  base,
			nu:   "example.com/a v1.0.0 h1:AAA=\nexample.com/a v1.0.0/go.mod h1:AAB=\n",
		},
		{
			// The same version now hashes differently: its content
			// changed (e.g. a moved tag on a private module).
			name: "hash changed for existing version",
			old:  base,
			nu:   "example.com/a v1.0.0 h1:XXX=\nexample.com/a v1.0.0/go.mod h1:AAB=\nexample.com/b v1.5.0 h1:BBB=\nexample.com/b v1.5.0/go.mod h1:BBC=\n",
			want: []string{"example.com/a"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := diffGoSum([]byte(tc.old), []byte(tc.nu)); !slices.Equal(got, tc.want) {
				t.Errorf("diffGoSum = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestDiffGoWork(t *testing.T) {
	base := "go 1.21\n\nuse (\n\t.\n\t./tools\n)\n\nreplace example.com/b v1.0.0 => ../b\n"

	cases := []struct {
		name    string
		old, nu string
		want    goWorkDiff
		wantErr bool
	}{
		{name: "identical", old: base, nu: base},
		{
			name: "reordered uses",
			old:  base,
			nu:   "go 1.21\n\nuse (\n\t./tools\n\t.\n)\n\nreplace example.com/b v1.0.0 => ../b\n",
		},
		{
			name: "use added",
			old:  base,
			nu:   "go 1.21\n\nuse (\n\t.\n\t./tools\n\t./extra\n)\n\nreplace example.com/b v1.0.0 => ../b\n",
			want: goWorkDiff{addedUses: []string{"extra"}},
		},
		{
			name: "use removed",
			old:  base,
			nu:   "go 1.21\n\nuse .\n\nreplace example.com/b v1.0.0 => ../b\n",
			want: goWorkDiff{removedUses: []string{"tools"}},
		},
		{
			name: "replace changed",
			old:  base,
			nu:   "go 1.21\n\nuse (\n\t.\n\t./tools\n)\n\nreplace example.com/b v1.0.0 => ../b2\n",
			want: goWorkDiff{modules: []string{"example.com/b"}},
		},
		{
			name: "go directive changed",
			old:  base,
			nu:   "go 1.22\n\nuse (\n\t.\n\t./tools\n)\n\nreplace example.com/b v1.0.0 => ../b\n",
			want: goWorkDiff{all: true},
		},
		{
			name:    "unparsable",
			old:     base,
			nu:      "use\n",
			wantErr: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := diffGoWork([]byte(tc.old), []byte(tc.nu))
			if (err != nil) != tc.wantErr {
				t.Fatalf("diffGoWork error = %v, wantErr %v", err, tc.wantErr)
			}
			if got.all != tc.want.all ||
				!slices.Equal(got.modules, tc.want.modules) ||
				!slices.Equal(got.addedUses, tc.want.addedUses) ||
				!slices.Equal(got.removedUses, tc.want.removedUses) {
				t.Errorf("diffGoWork = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestMemberImpact(t *testing.T) {
	base := "module example.com/root\n\ngo 1.21\n\nrequire (\n\texample.com/x v1.2.0\n\texample.com/y v1.0.0\n)\n"

	// The member itself always changes; x is raised above the base's
	// version, z is new to the graph, y is already satisfied by the base.
	member := "module example.com/member\n\ngo 1.21\n\nrequire (\n\texample.com/x v1.3.0\n\texample.com/y v1.0.0\n\texample.com/z v0.1.0\n)\n"
	mods, all, err := memberImpact([]byte(member), []byte(base))
	if err != nil || all {
		t.Fatalf("memberImpact = (_, %v, %v)", all, err)
	}
	want := []string{"example.com/member", "example.com/x", "example.com/z"}
	if !slices.Equal(mods, want) {
		t.Errorf("memberImpact = %v, want %v", mods, want)
	}

	// A pre-1.17 member does not record its full requirements.
	old := "module example.com/member\n\ngo 1.16\n"
	if _, all, err := memberImpact([]byte(old), []byte(base)); err != nil || !all {
		t.Errorf("memberImpact(go 1.16 member) = (_, %v, %v), want all=true", all, err)
	}

	// An unparsable member is an error.
	if _, _, err := memberImpact([]byte("module\n\ngo 1.21\n"), []byte(base)); err == nil {
		t.Error("memberImpact(unparsable member): got nil error, want error")
	}
}

func TestModGraphPruned(t *testing.T) {
	cases := []struct {
		name  string
		gomod string
		want  bool
	}{
		{"go 1.21", "module m\n\ngo 1.21\n", true},
		{"go 1.17", "module m\n\ngo 1.17\n", true},
		{"go 1.16", "module m\n\ngo 1.16\n", false},
		{"no go directive", "module m\n", false},
		{"unparsable", "module\n", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := modGraphPruned([]byte(tc.gomod)); got != tc.want {
				t.Errorf("modGraphPruned = %v, want %v", got, tc.want)
			}
		})
	}
}
