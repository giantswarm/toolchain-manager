// Command record-apkindex records the part of Alpine's package indexes the
// Alpine source's tests read: every package whose name contains one of the
// queries, and the dependency closure of each query's package, on every
// default branch.
//
// Usage: go run ./hack/record-apkindex <out-dir> <query>...
package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/giantswarm/toolchain-manager/internal/packages/alpine"
)

func main() {
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, "usage: record-apkindex <out-dir> <query>...")
		os.Exit(2)
	}
	if err := record(os.Args[1], os.Args[2:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func record(out string, queries []string) error {
	ctx := context.Background()
	repos := []string{"main", "community"}
	for _, b := range alpine.DefaultBranches {
		keep := map[string]bool{}
		src := alpine.New(alpine.Config{Branches: []alpine.Branch{b}})
		for _, q := range queries {
			found, err := src.Search(ctx, q)
			if err != nil {
				return err
			}
			for _, p := range found {
				keep[p.Name] = true
			}
			c, err := src.Closure(ctx, b.Name, []string{q})
			if err != nil {
				return err
			}
			for _, p := range c.Packages {
				keep[p.Name] = true
			}
		}
		for _, repo := range repos {
			if err := write(ctx, filepath.Join(out, b.Name, repo), b.Name, repo, keep); err != nil {
				return err
			}
		}
	}
	return nil
}

// write keeps the records of keep from the branch's repository index.
func write(ctx context.Context, dir, branch, repo string, keep map[string]bool) error {
	rc, err := alpine.HTTPFetcher{}.Fetch(ctx, branch, repo, "x86_64")
	if err != nil {
		return err
	}
	defer func() { _ = rc.Close() }()
	if err := os.MkdirAll(dir, 0o750); err != nil { //nolint:gosec // the output directory is the command's argument
		return err
	}
	var b strings.Builder
	var block []string
	flush := func() {
		for _, l := range block {
			if name, ok := strings.CutPrefix(l, "P:"); ok && keep[name] {
				b.WriteString(strings.Join(block, "\n") + "\n\n")
				break
			}
		}
		block = nil
	}
	sc := bufio.NewScanner(rc)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		if sc.Text() == "" {
			flush()
			continue
		}
		block = append(block, sc.Text())
	}
	flush()
	if err := sc.Err(); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "APKINDEX"), []byte(b.String()), 0o600) //nolint:gosec // the output directory is the command's argument
}
