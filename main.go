// Command goaffected reports which main packages are affected by the
// Go-related files (.go, go.mod, go.sum) changed in git.
//
// It is intended to be used in CI to decide which binaries need to be
// rebuilt or redeployed:
//
//	goaffected -base origin/main
package main

import (
	"flag"
	"fmt"
	"log"
	"os"
)

func main() {
	log.SetFlags(0)
	log.SetPrefix("goaffected: ")

	root := flag.String("C", ".", "root directory of the Go module to analyze")
	base := flag.String("base", "", "git ref to compare against; changed files are computed from the\nmerge-base of the ref and HEAD, including uncommitted and untracked\nfiles (default: the default branch that origin/HEAD points at)")
	head := flag.String("head", "", "commit whose changes to analyze; when set, only committed changes\nbetween the merge-base of -base and this commit are considered, so\nthe result is deterministic — the working tree and untracked files\nare ignored")
	includeCommentAndFormat := flag.Bool("include-comment-and-format", false, "also treat .go files whose changes are limited to comments or\nformatting as changed (by default such changes are ignored)")
	flag.Usage = func() {
		fmt.Fprint(flag.CommandLine.Output(), `Usage: goaffected [-C dir] [-base ref]

Prints the directories (relative to the module root) of main packages whose
build is affected by the files changed in git: the diff between the working
tree and the merge-base of -base and HEAD, plus untracked files. When -base
is not given, the default branch that origin/HEAD points at is used. With
-head, only the committed changes between -base and that commit are
considered, making the result deterministic.

Flags:
`)
		flag.PrintDefaults()
	}
	flag.Parse()
	if flag.NArg() > 0 {
		flag.Usage()
		os.Exit(2)
	}

	if *base == "" {
		ref, err := defaultBase(*root)
		if err != nil {
			log.Fatal(err)
		}
		*base = ref
	}
	ch, err := gitChanges(*root, *base, *head, !*includeCommentAndFormat)
	if err != nil {
		log.Fatal(err)
	}

	dirs, err := affectedMainDirs(*root, ch)
	if err != nil {
		log.Fatal(err)
	}
	for _, d := range dirs {
		fmt.Println(d)
	}
}
