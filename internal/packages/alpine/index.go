package alpine

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// record is one package of an APKINDEX.
type record struct {
	name        string
	version     string
	description string
	url         string
	license     string
	repository  string
	size        int64
	priority    int
	depends     []string
	provides    []string
}

// index is one branch's packages, every repository of it merged.
type index struct {
	packages map[string]*record
	// providers maps a provided name (cmd:helm, so:libz.so.1, a package
	// name) to the packages providing it.
	providers map[string][]*record
}

func newIndex() *index {
	return &index{packages: map[string]*record{}, providers: map[string][]*record{}}
}

func (ix *index) add(r *record) {
	if _, ok := ix.packages[r.name]; ok {
		return
	}
	ix.packages[r.name] = r
	ix.providers[r.name] = append(ix.providers[r.name], r)
	for _, p := range r.provides {
		n := depName(p)
		ix.providers[n] = append(ix.providers[n], r)
	}
}

// parseIndex reads an APKINDEX: blank-line separated records of one-letter
// fields (P name, V version, D depends, p provides, k provider priority).
func parseIndex(ix *index, repository string, rd io.Reader) error {
	sc := bufio.NewScanner(rd)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	r := &record{repository: repository}
	flush := func() {
		if r.name != "" {
			ix.add(r)
		}
		r = &record{repository: repository}
	}
	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			flush()
			continue
		}
		if len(line) < 2 || line[1] != ':' {
			continue
		}
		v := line[2:]
		switch line[0] {
		case 'P':
			r.name = v
		case 'V':
			r.version = v
		case 'T':
			r.description = v
		case 'U':
			r.url = v
		case 'L':
			r.license = v
		case 'I':
			r.size, _ = strconv.ParseInt(v, 10, 64)
		case 'k':
			r.priority, _ = strconv.Atoi(v)
		case 'D':
			r.depends = strings.Fields(v)
		case 'p':
			r.provides = strings.Fields(v)
		}
	}
	flush()
	return sc.Err()
}

// depName strips a dependency's or a provide's version constraint:
// python3~3.12, libssl3>=3.3.0 and cmd:helm=3.18.4-r5 name python3, libssl3
// and cmd:helm.
func depName(d string) string {
	if i := strings.IndexAny(d, "<>=~"); i > 0 {
		return d[:i]
	}
	return d
}

// Fetcher reads the APKINDEX of one repository of a branch for an
// architecture.
type Fetcher interface {
	Fetch(ctx context.Context, branch, repository, arch string) (io.ReadCloser, error)
}

// HTTPFetcher reads APKINDEX.tar.gz from an Alpine mirror.
type HTTPFetcher struct {
	// Mirror is the mirror's base URL, https://dl-cdn.alpinelinux.org/alpine
	// by default.
	Mirror string
	Client *http.Client
}

// Fetch downloads <mirror>/<branch>/<repository>/<arch>/APKINDEX.tar.gz and
// returns its APKINDEX.
func (f HTTPFetcher) Fetch(ctx context.Context, branch, repository, arch string) (io.ReadCloser, error) {
	mirror := f.Mirror
	if mirror == "" {
		mirror = "https://dl-cdn.alpinelinux.org/alpine"
	}
	client := f.Client
	if client == nil {
		client = http.DefaultClient
	}
	u := fmt.Sprintf("%s/%s/%s/%s/APKINDEX.tar.gz", strings.TrimSuffix(mirror, "/"), branch, repository, arch)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		_ = resp.Body.Close()
		return nil, fmt.Errorf("GET %s: %s", u, resp.Status)
	}
	rc, err := apkindexFromTarGz(resp.Body)
	if err != nil {
		_ = resp.Body.Close()
		return nil, fmt.Errorf("GET %s: %w", u, err)
	}
	return readCloser{Reader: rc, close: resp.Body.Close}, nil
}

// apkindexFromTarGz positions a reader on the APKINDEX entry of an
// APKINDEX.tar.gz (a signature archive and the index archive, concatenated).
func apkindexFromTarGz(r io.Reader) (io.Reader, error) {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return nil, err
	}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil, errors.New("no APKINDEX in the archive")
		}
		if err != nil {
			return nil, err
		}
		if h.Name == "APKINDEX" {
			return tr, nil
		}
	}
}

type readCloser struct {
	io.Reader
	close func() error
}

func (r readCloser) Close() error { return r.close() }

// DirFetcher reads recorded indexes from <dir>/<branch>/<repository>/APKINDEX,
// for tests and an offline manager; the architecture is the recording's.
type DirFetcher string

// Fetch opens the recorded APKINDEX; a repository not recorded is empty.
func (d DirFetcher) Fetch(_ context.Context, branch, repository, _ string) (io.ReadCloser, error) {
	f, err := os.Open(filepath.Join(string(d), branch, repository, "APKINDEX")) //nolint:gosec // the recording directory is the caller's
	if errors.Is(err, os.ErrNotExist) {
		return io.NopCloser(strings.NewReader("")), nil
	}
	return f, err
}
