package main

import "testing"

func TestSameGoTokens(t *testing.T) {
	base := "package p\n\nfunc F() int {\n\treturn 1\n}\n"

	cases := []struct {
		name string
		a, b string
		want bool
	}{
		{"identical", base, base, true},
		{
			"comment added",
			base,
			"package p\n\n// F returns one.\nfunc F() int {\n\treturn 1 // one\n}\n",
			true,
		},
		{
			"blank lines and indentation",
			base,
			"package p\nfunc F() int {\n\n\n  return 1\n}\n",
			true,
		},
		{
			"one-line block reflowed",
			"package p\n\nfunc F() int { return 1 }\n",
			base,
			true,
		},
		{
			"code changed",
			base,
			"package p\n\nfunc F() int {\n\treturn 2\n}\n",
			false,
		},
		{
			"build tag added",
			base,
			"//go:build linux\n\n" + base,
			false,
		},
		{
			"embed directive changed",
			"package p\n\nimport _ \"embed\"\n\n//go:embed a.txt\nvar s string\n",
			"package p\n\nimport _ \"embed\"\n\n//go:embed b.txt\nvar s string\n",
			false,
		},
		{
			"cgo preamble changed",
			"package p\n\n/*\n#include <stdio.h>\n*/\nimport \"C\"\n",
			"package p\n\n/*\n#include <stdlib.h>\n*/\nimport \"C\"\n",
			false,
		},
		{
			"syntax error",
			base,
			"package p\n\nfunc F( {\n",
			false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := sameGoTokens([]byte(tc.a), []byte(tc.b)); got != tc.want {
				t.Errorf("sameGoTokens = %v, want %v", got, tc.want)
			}
		})
	}
}
