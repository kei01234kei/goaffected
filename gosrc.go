package main

import (
	"go/scanner"
	"go/token"
	"slices"
	"strings"
)

// sameGoTokens reports whether two Go source files scan to identical token
// streams, i.e. they differ only in comments or formatting and therefore
// build identically. Compiler directives (//go:build, //go:embed, //line,
// //export, ...) are kept in the stream because they do affect the build,
// and files using cgo are never considered equal because their C preamble
// lives in comments. On any doubt (e.g. scan errors) it returns false.
func sameGoTokens(a, b []byte) bool {
	ta, okA := goTokens(a)
	tb, okB := goTokens(b)
	return okA && okB && slices.Equal(ta, tb)
}

type tokLit struct {
	tok token.Token
	lit string
}

func goTokens(src []byte) ([]tokLit, bool) {
	fset := token.NewFileSet()
	file := fset.AddFile("", fset.Base(), len(src))
	failed := false
	var s scanner.Scanner
	s.Init(file, src, func(token.Position, string) { failed = true }, scanner.ScanComments)

	var toks []tokLit
	for {
		_, tok, lit := s.Scan()
		if tok == token.EOF {
			break
		}
		switch tok {
		case token.COMMENT:
			if !isDirective(lit) {
				continue
			}
		case token.SEMICOLON:
			lit = ";" // normalize auto-inserted "\n" semicolons
		case token.STRING:
			if lit == `"C"` || lit == "`C`" {
				return nil, false // cgo: the C preamble is a comment
			}
		}
		toks = append(toks, tokLit{tok, lit})
	}
	if failed {
		return nil, false
	}

	// Drop the optional semicolons before ) and } so that, for example,
	// splitting a one-line block across lines compares equal.
	var out []tokLit
	for i, t := range toks {
		if t.tok == token.SEMICOLON && i+1 < len(toks) &&
			(toks[i+1].tok == token.RPAREN || toks[i+1].tok == token.RBRACE) {
			continue
		}
		out = append(out, t)
	}
	return out, true
}

// isDirective reports whether the comment (including its // or /* markers)
// is a compiler or tool directive rather than prose.
func isDirective(c string) bool {
	if strings.HasPrefix(c, "//line ") || strings.HasPrefix(c, "/*line ") {
		return true
	}
	if strings.HasPrefix(c, "//export ") || strings.HasPrefix(c, "//extern ") {
		return true
	}
	if !strings.HasPrefix(c, "//") {
		return false
	}
	// //tool:directive — lowercase word, no space, before the first colon.
	body := c[2:]
	i := strings.Index(body, ":")
	if i <= 0 {
		return false
	}
	for _, r := range body[:i] {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') {
			return false
		}
	}
	return true
}
