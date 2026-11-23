// Package selfupdate は mtb upgrade の中身です。GitHub のリリースから最新版を調べ、
// 配布物を落としてチェックサムを照らし、実行ファイルを入れ替えます。
//
// 外の世界（HTTP・ファイル）にはすべて引数の口から触ります。テストは偽物で回します。
// **最新版の調べ方はこのファイルの Latest だけにあります。** 更新元を変えるときはここを直す。
package selfupdate

import (
	"archive/tar"
	"archive/zip"
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"path"
	"strconv"
	"strings"
)

const (
	// Repo はリリースを置いている GitHub のリポジトリです。
	Repo = "ideamans/mtb"
	// LatestURL は最新のリリースを返す API です。
	LatestURL = "https://api.github.com/repos/" + Repo + "/releases/latest"
	// DownloadBase は配布物の置き場です（/v<版>/<ファイル名>）。
	DownloadBase = "https://github.com/" + Repo + "/releases/download"
	// maxDownload は落とすものの大きさの上限です（壊れた応答で手元を埋めないため）。
	maxDownload = 256 << 20
)

// Doer は HTTP の送信手段です（api.Doer と同じ形）。
type Doer interface {
	Do(req *http.Request) (*http.Response, error)
}

// ErrSource は更新元に届かない・見つからない・照合できないことを表します。
// これに当たったら何も書き換えずに終わり、入れ直しを案内します。
var ErrSource = errors.New("update source unavailable")

func get(ctx context.Context, d Doer, url, userAgent, accept string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	resp, err := d.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: GET %s: %w", ErrSource, url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%w: GET %s: HTTP %d", ErrSource, url, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxDownload+1))
	if err != nil {
		return nil, fmt.Errorf("%w: GET %s: %w", ErrSource, url, err)
	}
	if len(body) > maxDownload {
		return nil, fmt.Errorf("%w: GET %s: too large", ErrSource, url)
	}
	return body, nil
}

// Latest は最新のリリースの版（先頭の v は外す）を返します。
func Latest(ctx context.Context, d Doer, userAgent string) (string, error) {
	body, err := get(ctx, d, LatestURL, userAgent, "application/vnd.github+json")
	if err != nil {
		return "", err
	}
	var r struct {
		TagName string `json:"tag_name"`
	}
	if err := json.Unmarshal(body, &r); err != nil || r.TagName == "" {
		return "", fmt.Errorf("%w: no tag_name in %s", ErrSource, LatestURL)
	}
	v := strings.TrimPrefix(r.TagName, "v")
	if _, ok := parse(v); !ok {
		return "", fmt.Errorf("%w: unexpected tag %q", ErrSource, r.TagName)
	}
	return v, nil
}

// ArchiveName は goreleaser の name_template に合わせた配布物の名前です。
func ArchiveName(version, goos, goarch string) string {
	ext := ".tar.gz"
	if goos == "windows" {
		ext = ".zip"
	}
	return fmt.Sprintf("mtb_%s_%s_%s%s", version, goos, goarch, ext)
}

// ChecksumsName は goreleaser のチェックサムの一覧の名前です。
func ChecksumsName(version string) string { return fmt.Sprintf("mtb_%s_checksums.txt", version) }

// Fetch は配布物を落とし、チェックサムを照らし、中の実行ファイルを取り出します。
func Fetch(ctx context.Context, d Doer, userAgent, version, goos, goarch string) ([]byte, error) {
	base := DownloadBase + "/v" + version + "/"
	sums, err := get(ctx, d, base+ChecksumsName(version), userAgent, "")
	if err != nil {
		return nil, err
	}
	name := ArchiveName(version, goos, goarch)
	want, err := lookupSum(sums, name)
	if err != nil {
		return nil, err
	}
	archive, err := get(ctx, d, base+name, userAgent, "")
	if err != nil {
		return nil, err
	}
	got := sha256.Sum256(archive)
	if hex.EncodeToString(got[:]) != want {
		return nil, fmt.Errorf("%w: checksum mismatch for %s", ErrSource, name)
	}
	bin := "mtb"
	if goos == "windows" {
		bin = "mtb.exe"
	}
	data, err := extract(archive, goos == "windows", bin)
	if err != nil {
		return nil, fmt.Errorf("%w: %s: %w", ErrSource, name, err)
	}
	return data, nil
}

func lookupSum(sums []byte, name string) (string, error) {
	sc := bufio.NewScanner(bytes.NewReader(sums))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) == 2 && strings.TrimPrefix(f[1], "*") == name && len(f[0]) == 64 {
			return strings.ToLower(f[0]), nil
		}
	}
	return "", fmt.Errorf("%w: %s is not in the checksums", ErrSource, name)
}

func extract(archive []byte, isZip bool, bin string) ([]byte, error) {
	if isZip {
		zr, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
		if err != nil {
			return nil, err
		}
		for _, f := range zr.File {
			if path.Base(f.Name) == bin && !f.FileInfo().IsDir() {
				rc, err := f.Open()
				if err != nil {
					return nil, err
				}
				defer rc.Close()
				return io.ReadAll(io.LimitReader(rc, maxDownload))
			}
		}
		return nil, fmt.Errorf("%s is not in the archive", bin)
	}
	gz, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return nil, err
	}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return nil, fmt.Errorf("%s is not in the archive", bin)
		}
		if err != nil {
			return nil, err
		}
		if h.Typeflag == tar.TypeReg && path.Base(h.Name) == bin {
			return io.ReadAll(io.LimitReader(tr, maxDownload))
		}
	}
}

// Compare は版を比べます（a<b なら負、同じなら 0、a>b なら正）。数字の3つ組だけを見ます。
func Compare(a, b string) int {
	pa, _ := parse(a)
	pb, _ := parse(b)
	for i := 0; i < 3; i++ {
		if pa[i] != pb[i] {
			if pa[i] < pb[i] {
				return -1
			}
			return 1
		}
	}
	return 0
}

func parse(v string) ([3]int, bool) {
	var out [3]int
	parts := strings.Split(strings.TrimPrefix(v, "v"), ".")
	if len(parts) != 3 {
		return out, false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return out, false
		}
		out[i] = n
	}
	return out, true
}

// Valid は「x.y.z」の形の版かを返します。
func Valid(v string) bool { _, ok := parse(v); return ok }

// Manager はパッケージマネージャの種類です。空なら手で置いたもの（自分で入れ替えてよい）。
type Manager string

const (
	ManagerNone       Manager = ""
	ManagerHomebrew   Manager = "homebrew"
	ManagerSystem     Manager = "apt/yum"
	ManagerScoop      Manager = "scoop"
	ManagerChocolatey Manager = "chocolatey"
)

// DetectManager は実行ファイルの場所から、パッケージマネージャで入れたものかを見分けます。
// インストールスクリプトの既定（/usr/local/bin）と ~/.local/bin は手で置いたものとみなします。
func DetectManager(exe string) Manager {
	p := strings.ToLower(strings.ReplaceAll(exe, "\\", "/"))
	switch {
	case strings.Contains(p, "/cellar/") || strings.Contains(p, "/homebrew/") || strings.Contains(p, "/linuxbrew/"):
		return ManagerHomebrew
	case strings.Contains(p, "/scoop/"):
		return ManagerScoop
	case strings.Contains(p, "/chocolatey/"):
		return ManagerChocolatey
	case strings.HasPrefix(p, "/usr/bin/") || strings.HasPrefix(p, "/bin/") || strings.HasPrefix(p, "/usr/sbin/"):
		return ManagerSystem
	}
	return ManagerNone
}

// Command はパッケージマネージャで更新するときに打つコマンドです。
func (m Manager) Command() []string {
	switch m {
	case ManagerHomebrew:
		return []string{"brew upgrade mtb"}
	case ManagerScoop:
		return []string{"scoop update mtb"}
	case ManagerChocolatey:
		return []string{"choco upgrade mtb"}
	case ManagerSystem:
		return []string{"sudo apt update && sudo apt install --only-upgrade mtb", "sudo yum update mtb"}
	}
	return nil
}

// FS は入れ替えに使うファイル操作です。
type FS interface {
	Stat(name string) (fs.FileMode, error)
	WriteFile(name string, data []byte, perm fs.FileMode) error
	Rename(oldpath, newpath string) error
	Remove(name string) error
}

// Replace は exe を data で入れ替えます。隣に書いてから rename するので、途中で落ちても
// 元の実行ファイルは壊れません。Windows は動いている実行ファイルを上書きできないので、
// 元を <exe>.old へ退かしてから置きます（.old は次の更新のときに消します）。
func Replace(fsys FS, exe string, data []byte, windows bool) error {
	mode, err := fsys.Stat(exe)
	if err != nil {
		return err
	}
	tmp := exe + ".new"
	_ = fsys.Remove(tmp)
	if err := fsys.WriteFile(tmp, data, mode.Perm()|0o100); err != nil {
		return err
	}
	if windows {
		old := exe + ".old"
		_ = fsys.Remove(old)
		if err := fsys.Rename(exe, old); err != nil {
			_ = fsys.Remove(tmp)
			return err
		}
		if err := fsys.Rename(tmp, exe); err != nil {
			_ = fsys.Rename(old, exe)
			_ = fsys.Remove(tmp)
			return err
		}
		return nil
	}
	if err := fsys.Rename(tmp, exe); err != nil {
		_ = fsys.Remove(tmp)
		return err
	}
	return nil
}
