package cmd

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io/fs"
	"strings"
	"testing"
)

// memSelf は実行ファイルの入れ替えを記録する偽物です。
type memSelf struct {
	files   map[string][]byte
	modes   map[string]fs.FileMode
	failOn  string // この名前への書き込み・rename を失敗させる
	renames []string
}

func newMemSelf(exe string) *memSelf {
	return &memSelf{
		files: map[string][]byte{exe: []byte("OLD")},
		modes: map[string]fs.FileMode{exe: 0o755},
	}
}

func (m *memSelf) Stat(name string) (fs.FileMode, error) {
	if _, ok := m.files[name]; !ok {
		return 0, fs.ErrNotExist
	}
	return m.modes[name], nil
}

func (m *memSelf) WriteFile(name string, data []byte, perm fs.FileMode) error {
	if name == m.failOn {
		return fs.ErrPermission
	}
	m.files[name] = data
	m.modes[name] = perm
	return nil
}

func (m *memSelf) Rename(oldpath, newpath string) error {
	if newpath == m.failOn {
		return fs.ErrPermission
	}
	d, ok := m.files[oldpath]
	if !ok {
		return fs.ErrNotExist
	}
	m.files[newpath], m.modes[newpath] = d, m.modes[oldpath]
	delete(m.files, oldpath)
	m.renames = append(m.renames, oldpath+">"+newpath)
	return nil
}

func (m *memSelf) Remove(name string) error {
	if _, ok := m.files[name]; !ok {
		return fs.ErrNotExist
	}
	delete(m.files, name)
	return nil
}

func tarGz(t *testing.T, name string, data []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, f := range []struct {
		n string
		b []byte
	}{{"LICENSE", []byte("x")}, {name, data}} {
		if err := tw.WriteHeader(&tar.Header{Name: f.n, Mode: 0o755, Size: int64(len(f.b)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		tw.Write(f.b)
	}
	tw.Close()
	gz.Close()
	return buf.Bytes()
}

func zipOf(t *testing.T, name string, data []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create(name)
	if err != nil {
		t.Fatal(err)
	}
	w.Write(data)
	zw.Close()
	return buf.Bytes()
}

func sumLine(archive []byte, name string) string {
	s := sha256.Sum256(archive)
	return hex.EncodeToString(s[:]) + "  " + name + "\n"
}

// releaseBuild は「リリースの配布物」として動かします（テストの間だけ）。
func releaseBuild(t *testing.T, version string) {
	t.Helper()
	ov, oc := Version, BuildChannel
	Version, BuildChannel = version, "release"
	t.Cleanup(func() { Version, BuildChannel = ov, oc })
}

func upgradeHarness(t *testing.T, exe, goos string) (*harness, *memSelf) {
	h := newHarness(t)
	self := newMemSelf(exe)
	h.env.Executable = func() (string, error) { return exe, nil }
	h.env.SelfFS = self
	h.env.GOOS, h.env.GOARCH = goos, "amd64"
	return h, self
}

func latestStub(tag string) stub { return okJSON(`{"tag_name":"` + tag + `"}`) }

func TestUpgradeReplacesManualInstall(t *testing.T) {
	releaseBuild(t, "0.2.0")
	h, self := upgradeHarness(t, "/home/u/.local/bin/mtb", "linux")
	archive := tarGz(t, "mtb", []byte("NEW"))
	h.reply(latestStub("v0.3.0"),
		stub{status: 200, body: sumLine([]byte("other"), "mtb_0.3.0_darwin_amd64.tar.gz") + sumLine(archive, "mtb_0.3.0_linux_amd64.tar.gz")},
		stub{status: 200, body: string(archive)})
	out := h.mustRun("upgrade")
	if !strings.Contains(out, "0.2.0 → 0.3.0") {
		t.Fatalf("出力: %q", out)
	}
	if got := string(self.files["/home/u/.local/bin/mtb"]); got != "NEW" {
		t.Fatalf("入れ替わっていない: %q", got)
	}
	if _, left := self.files["/home/u/.local/bin/mtb.new"]; left {
		t.Fatal(".new が残っている")
	}
	if self.modes["/home/u/.local/bin/mtb"]&0o100 == 0 {
		t.Fatal("実行権が無い")
	}
	urls := []string{}
	for _, s := range h.doer.sent {
		urls = append(urls, s.URL)
	}
	want := []string{
		"https://api.github.com/repos/ideamans/mtb/releases/latest",
		"https://github.com/ideamans/mtb/releases/download/v0.3.0/mtb_0.3.0_checksums.txt",
		"https://github.com/ideamans/mtb/releases/download/v0.3.0/mtb_0.3.0_linux_amd64.tar.gz",
	}
	if strings.Join(urls, "\n") != strings.Join(want, "\n") {
		t.Fatalf("取りに行った先:\n%s", strings.Join(urls, "\n"))
	}
}

func TestUpgradeWindowsMovesOldAside(t *testing.T) {
	releaseBuild(t, "0.2.0")
	exe := `C:\Users\u\bin\mtb.exe`
	h, self := upgradeHarness(t, exe, "windows")
	archive := zipOf(t, "mtb.exe", []byte("NEW"))
	h.reply(latestStub("v0.3.0"), stub{status: 200, body: sumLine(archive, "mtb_0.3.0_windows_amd64.zip")}, stub{status: 200, body: string(archive)})
	h.mustRun("upgrade")
	if string(self.files[exe]) != "NEW" || string(self.files[exe+".old"]) != "OLD" {
		t.Fatalf("入れ替え: %v", self.renames)
	}
}

func TestUpgradeCheckAndSameAndOlder(t *testing.T) {
	releaseBuild(t, "0.3.0")
	h, self := upgradeHarness(t, "/usr/local/bin/mtb", "linux")

	h.reply(latestStub("v0.3.0"))
	if out := h.mustRun("upgrade"); !strings.Contains(out, "最新版です") {
		t.Fatalf("同じ版: %q", out)
	}
	h.reply(latestStub("v0.2.9"))
	if out := h.mustRun("upgrade"); !strings.Contains(out, "何もしません") {
		t.Fatalf("手元が新しい: %q", out)
	}
	h.reply(latestStub("v0.10.0"))
	out := h.mustRun("upgrade", "--check", "--format", "json")
	if !strings.Contains(out, `"status": "available"`) || !strings.Contains(out, `"latest": "0.10.0"`) {
		t.Fatalf("--check: %q", out)
	}
	if len(h.doer.sent) != 3 || string(self.files["/usr/local/bin/mtb"]) != "OLD" {
		t.Fatal("--check で落としたか書き換えた")
	}
}

func TestUpgradeChecksumMismatchChangesNothing(t *testing.T) {
	releaseBuild(t, "0.2.0")
	h, self := upgradeHarness(t, "/usr/local/bin/mtb", "linux")
	archive := tarGz(t, "mtb", []byte("NEW"))
	h.reply(latestStub("v0.3.0"),
		stub{status: 200, body: sumLine([]byte("tampered"), "mtb_0.3.0_linux_amd64.tar.gz")},
		stub{status: 200, body: string(archive)})
	err := h.run("upgrade")
	if err == nil || !strings.Contains(err.Error(), "checksum") || !strings.Contains(err.Error(), installGuideURL) {
		t.Fatalf("エラー: %v", err)
	}
	if string(self.files["/usr/local/bin/mtb"]) != "OLD" || len(self.files) != 1 {
		t.Fatalf("書き換えた: %v", self.files)
	}
}

func TestUpgradeSourceUnavailable(t *testing.T) {
	releaseBuild(t, "0.2.0")
	// 最新版が見つからない（更新元が移った）
	h, self := upgradeHarness(t, "/usr/local/bin/mtb", "linux")
	h.reply(stub{status: 404, body: "Not Found"})
	err := h.run("upgrade")
	if err == nil || !strings.Contains(err.Error(), installGuideURL) || ExitCode(err) == ExitOK {
		t.Fatalf("404: %v", err)
	}
	// 届かない
	h.doer.err = errors.New("dial tcp: connection refused")
	if err := h.run("upgrade", "--check"); err == nil || !strings.Contains(err.Error(), installGuideURL) {
		t.Fatalf("届かない: %v", err)
	}
	if string(self.files["/usr/local/bin/mtb"]) != "OLD" {
		t.Fatal("書き換えた")
	}
}

func TestUpgradeLeavesPackageManagersAlone(t *testing.T) {
	releaseBuild(t, "0.2.0")
	cases := map[string]string{
		"/usr/bin/mtb":                              "apt install --only-upgrade mtb",
		"/opt/homebrew/Cellar/mtb/0.2.0/bin/mtb":    "brew upgrade mtb",
		`C:\Users\u\scoop\apps\mtb\current\mtb.exe`: "scoop update mtb",
	}
	for exe, cmdline := range cases {
		h, self := upgradeHarness(t, exe, "linux")
		h.reply(latestStub("v0.3.0"))
		err := h.run("upgrade")
		if err == nil || !strings.Contains(h.out.String(), cmdline) {
			t.Fatalf("%s: err=%v out=%q", exe, err, h.out.String())
		}
		if len(h.doer.sent) != 1 || string(self.files[exe]) != "OLD" {
			t.Fatalf("%s: 書き換えようとした", exe)
		}
		// --check は案内だけで成功
		h.reply(latestStub("v0.3.0"))
		if err := h.run("upgrade", "--check"); err != nil {
			t.Fatalf("%s --check: %v", exe, err)
		}
	}
}

func TestUpgradeRefusesSourceBuild(t *testing.T) {
	ov, oc := Version, BuildChannel
	Version, BuildChannel = "0.2.0", ""
	t.Cleanup(func() { Version, BuildChannel = ov, oc })
	h, self := upgradeHarness(t, "/home/u/go/bin/mtb", "linux")
	err := h.run("upgrade")
	if err == nil || !strings.Contains(h.out.String(), "go install github.com/ideamans/mtb/cmd/mtb@latest") {
		t.Fatalf("err=%v out=%q", err, h.out.String())
	}
	if len(h.doer.sent) != 0 || string(self.files["/home/u/go/bin/mtb"]) != "OLD" {
		t.Fatal("ソースからのビルドで問い合わせたか書き換えた")
	}
}

func TestUpgradeWriteFailureKeepsOld(t *testing.T) {
	releaseBuild(t, "0.2.0")
	h, self := upgradeHarness(t, "/usr/local/bin/mtb", "linux")
	self.failOn = "/usr/local/bin/mtb.new"
	archive := tarGz(t, "mtb", []byte("NEW"))
	h.reply(latestStub("v0.3.0"), stub{status: 200, body: sumLine(archive, "mtb_0.3.0_linux_amd64.tar.gz")}, stub{status: 200, body: string(archive)})
	err := h.run("upgrade")
	if err == nil || !strings.Contains(err.Error(), "sudo mtb upgrade") {
		t.Fatalf("エラー: %v", err)
	}
	if string(self.files["/usr/local/bin/mtb"]) != "OLD" {
		t.Fatal("壊した")
	}
}
