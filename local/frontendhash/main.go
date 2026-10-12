// public/ の静的ファイルから、ベンチマーカーが埋め込む frontend_hashes.json と frontend_files.json を作る。
//
// 公式リポジトリの frontend/vite.config.ts にある generateHashesFile と同じ処理。
// AMI のビルドではフロントエンドを作り直した直後にこれが走り、その結果を埋め込んでベンチがビルドされる。
// 公式リポジトリにコミットされている JSON は AMI のフロントエンドと一致しないので、ローカルではこれで作り直す。
//
// 使い方: go run ./frontendhash/main.go <public ディレクトリ> <出力先 (bench/benchrun)>
package main

import (
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

type route struct {
	CSS      []string `json:"css"`
	Imports  []string `json:"imports"`
	Module   string   `json:"module"`
	ParentID *string  `json:"parentId"`
	Path     *string  `json:"path"`
}

type manifest struct {
	Entry  route            `json:"entry"`
	Routes map[string]route `json:"routes"`
}

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: frontendhash <public ディレクトリ> <出力先ディレクトリ>")
		os.Exit(2)
	}
	if err := run(os.Args[1], os.Args[2]); err != nil {
		fmt.Fprintln(os.Stderr, "frontendhash:", err)
		os.Exit(1)
	}
}

func run(publicDir, outDir string) error {
	var files []string
	err := filepath.WalkDir(publicDir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, err := filepath.Rel(publicDir, p)
		if err != nil {
			return err
		}
		files = append(files, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		return err
	}

	hashes := map[string]string{}
	for _, f := range files {
		if f == ".vite/manifest.json" {
			continue
		}
		b, err := os.ReadFile(filepath.Join(publicDir, f))
		if err != nil {
			return err
		}
		sum := md5.Sum(b)
		hashes[f] = hex.EncodeToString(sum[:])
	}
	if err := writeJSON(filepath.Join(outDir, "frontend_hashes.json"), hashes); err != nil {
		return err
	}

	m, err := readManifest(publicDir, files)
	if err != nil {
		return err
	}

	var favicons, assetsForMap []string
	for _, f := range files {
		if f == "favicon.ico" || f == "favicon-32x32.png" {
			favicons = append(favicons, "/"+f)
		}
		if strings.HasPrefix(f, "images/buildings") || strings.HasPrefix(f, "images/house") || strings.HasPrefix(f, "images/town") {
			assetsForMap = append(assetsForMap, "/"+f)
		}
	}

	modulesForEachPath := map[string][]string{}
	for _, r := range m.Routes {
		if r.Path == nil {
			continue
		}
		list := filesOf(m.Entry)
		list = append(list, filesWithAncestors(r, m.Routes)...)
		list = append(list, favicons...)
		if *r.Path == "client" {
			list = append(list, assetsForMap...)
		}
		modulesForEachPath["/"+*r.Path] = unique(list)
	}
	return writeJSON(filepath.Join(outDir, "frontend_files.json"), modulesForEachPath)
}

func readManifest(publicDir string, files []string) (*manifest, error) {
	for _, f := range files {
		if !strings.HasPrefix(f, "assets/manifest-") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(publicDir, f))
		if err != nil {
			return nil, err
		}
		s := strings.TrimSpace(string(b))
		if !strings.HasPrefix(s, "window.__remixManifest=") {
			return nil, fmt.Errorf("%s は Remix のマニフェストではありません", f)
		}
		s = strings.TrimSuffix(strings.TrimPrefix(s, "window.__remixManifest="), ";")
		var m manifest
		if err := json.Unmarshal([]byte(s), &m); err != nil {
			return nil, fmt.Errorf("%s を読めません: %w", f, err)
		}
		return &m, nil
	}
	return nil, fmt.Errorf("assets/manifest-*.js が見つかりません")
}

func filesOf(r route) []string {
	return append(append([]string{r.Module}, r.Imports...), r.CSS...)
}

func filesWithAncestors(r route, routes map[string]route) []string {
	list := filesOf(r)
	if r.ParentID != nil {
		list = append(list, filesWithAncestors(routes[*r.ParentID], routes)...)
	}
	return list
}

func unique(list []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, s := range list {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

func writeJSON(path string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o644)
}
