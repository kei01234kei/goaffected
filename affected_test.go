package main

import (
	"path/filepath"
	"slices"
	"testing"
)

func TestAffectedMainDirs(t *testing.T) {
	root := filepath.Join("testdata", "proj")

	cases := []struct {
		name string
		ch   changes
		want []string
	}{
		{"shared package", changes{files: []string{"pkg/util/util.go"}}, []string{"cmd/a"}},
		{"single main", changes{files: []string{"cmd/b/main.go"}}, []string{"cmd/b"}},
		{"both", changes{files: []string{"pkg/util/util.go", "cmd/b/main.go"}}, []string{"cmd/a", "cmd/b"}},
		{"test file only", changes{files: []string{"pkg/util/util_test.go"}}, nil},
		{"unrelated file", changes{files: []string{"README.md"}}, nil},
		{"deleted file in existing package", changes{files: []string{"pkg/util/deleted.go"}}, []string{"cmd/a"}},
		{"deleted assembly file", changes{files: []string{"pkg/util/asm_amd64.s"}}, []string{"cmd/a"}},
		{"deleted cgo source", changes{files: []string{"pkg/util/util.c"}}, []string{"cmd/a"}},
		{"deleted syso object", changes{files: []string{"cmd/b/rsrc_windows_amd64.syso"}}, []string{"cmd/b"}},
		{"non-source file in package", changes{files: []string{"pkg/util/notes.txt"}}, nil},
		{"changed dependency module", changes{modules: []string{"example.com/dep"}}, []string{"cmd/c"}},
		{"unknown module", changes{modules: []string{"example.com/unused"}}, nil},
		{"all packages", changes{all: true}, []string{"cmd/a", "cmd/b", "cmd/c"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := affectedMainDirs(root, tc.ch)
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(got, tc.want) {
				t.Errorf("affectedMainDirs(%+v) = %v, want %v", tc.ch, got, tc.want)
			}
		})
	}
}
