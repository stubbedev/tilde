// Command scanner generates Wayland protocol bindings from the XML listed in
// internal/protocol/protocols.json.
package main

//go:generate go run . -dir ../protocol

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
)

func main() {
	dir := flag.String("dir", "internal/protocol", "directory holding protocols.json")
	fetch := flag.Bool("fetch", false, "download the XML named by the manifest instead of generating code")
	flag.Parse()
	if err := run(*dir, *fetch); err != nil {
		fmt.Fprintln(os.Stderr, "scanner:", err)
		os.Exit(1)
	}
}

func run(dir string, fetch bool) error {
	m, err := loadManifest(dir)
	if err != nil {
		return err
	}
	if fetch {
		return fetchAll(m)
	}
	module, err := modulePath(dir)
	if err != nil {
		return err
	}
	protocols, err := loadProtocols(m)
	if err != nil {
		return err
	}
	for _, s := range sides {
		files := map[string][]byte{}
		for _, p := range protocols {
			src, err := generate(p, s, module)
			if err != nil {
				return err
			}
			files[path.Join(p.pkg, "protocol.go")] = src
		}
		if err := replaceTree(filepath.Join(dir, s.dir()), files); err != nil {
			return err
		}
	}
	return nil
}

func loadProtocols(m *manifest) ([]*protocol, error) {
	var protocols []*protocol
	for _, e := range m.Protocols {
		f, err := os.Open(m.xmlPath(e))
		if err != nil {
			return nil, fmt.Errorf("open protocol XML: %w", err)
		}
		p, err := parseProtocol(f, path.Join("xml", e.Source, e.Path), e.Package)
		_ = f.Close()
		if err != nil {
			return nil, err
		}
		protocols = append(protocols, p)
	}
	if err := resolve(protocols); err != nil {
		return nil, err
	}
	return protocols, nil
}

func modulePath(dir string) (string, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", fmt.Errorf("resolve %s: %w", dir, err)
	}
	for d := abs; ; d = filepath.Dir(d) {
		f, err := os.Open(filepath.Join(d, "go.mod"))
		if errors.Is(err, os.ErrNotExist) {
			if filepath.Dir(d) == d {
				return "", fmt.Errorf("no go.mod above %s", abs)
			}
			continue
		}
		if err != nil {
			return "", fmt.Errorf("open go.mod: %w", err)
		}
		defer f.Close()
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			if rest, ok := strings.CutPrefix(sc.Text(), "module "); ok {
				return strings.TrimSpace(rest), nil
			}
		}
		if err := sc.Err(); err != nil {
			return "", fmt.Errorf("read %s: %w", f.Name(), err)
		}
		return "", fmt.Errorf("%s has no module line", f.Name())
	}
}

func replaceTree(dst string, files map[string][]byte) error {
	tmp, err := os.MkdirTemp(filepath.Dir(dst), ".scanner-")
	if err != nil {
		return fmt.Errorf("create temporary tree for %s: %w", dst, err)
	}
	defer func() { _ = os.RemoveAll(tmp) }()
	for name, data := range files {
		p := filepath.Join(tmp, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return fmt.Errorf("create %s: %w", filepath.Dir(p), err)
		}
		if err := os.WriteFile(p, data, 0o644); err != nil {
			return fmt.Errorf("write %s: %w", p, err)
		}
	}
	if err := os.Chmod(tmp, 0o755); err != nil {
		return fmt.Errorf("chmod %s: %w", tmp, err)
	}
	if err := os.RemoveAll(dst); err != nil {
		return fmt.Errorf("remove old %s: %w", dst, err)
	}
	if err := os.Rename(tmp, dst); err != nil {
		return fmt.Errorf("move generated tree into %s: %w", dst, err)
	}
	return nil
}

const maxDownload = 4 << 20

func fetchAll(m *manifest) error {
	client := &http.Client{Timeout: 30 * time.Second}
	files := map[string][]byte{}
	for _, e := range m.Protocols {
		data, err := download(client, m.url(e))
		if err != nil {
			return err
		}
		files[path.Join(e.Source, e.Path)] = data
	}
	return replaceTree(filepath.Join(m.dir, "xml"), files)
}

func download(client *http.Client, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("build request for %s: %w", url, err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetch %s: %s", url, resp.Status)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxDownload+1))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", url, err)
	}
	if len(data) > maxDownload {
		return nil, fmt.Errorf("read %s: larger than %d bytes", url, maxDownload)
	}
	return data, nil
}
