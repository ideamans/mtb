package logs

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

var fixedNow = time.Date(2026, 11, 23, 3, 0, 0, 0, time.UTC)

func day(offset int) string { return fixedNow.AddDate(0, 0, -offset).Format(dateLayout) }

func TestAppendWritesDailyFilesAndGitignore(t *testing.T) {
	dir := t.TempDir()
	l := New(OSFS{}, dir, func() time.Time { return fixedNow })
	if err := l.Append(Audit, []byte(`{"a":1}`)); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "audit", "2026-11-23.jsonl"))
	if err != nil || string(b) != "{\"a\":1}\n" {
		t.Fatalf("書いた中身 = %q, %v", b, err)
	}
	if st, _ := os.Stat(filepath.Join(dir, "audit", "2026-11-23.jsonl")); st.Mode().Perm() != 0o600 {
		t.Errorf("権限 = %v（0600 のはず）", st.Mode().Perm())
	}
	ig, err := os.ReadFile(filepath.Join(dir, ".gitignore"))
	if err != nil || !strings.Contains(string(ig), "*") {
		t.Errorf(".gitignore = %q, %v", ig, err)
	}
}

// 並んで動く書き手（goroutine）が、1行も混ぜずに書けること
func TestConcurrentAppendsDoNotInterleave(t *testing.T) {
	dir := t.TempDir()
	l := New(OSFS{}, dir, func() time.Time { return fixedNow })
	const writers, lines = 20, 100
	pad := strings.Repeat("x", 3000) // パイプの原子性（4KB）を超えない程度に長く
	var wg sync.WaitGroup
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < lines; i++ {
				_ = l.Append(Run, []byte(fmt.Sprintf(`{"w":%d,"i":%d,"pad":%q}`, w, i, pad)))
			}
		}(w)
	}
	wg.Wait()
	checkLines(t, l, writers*lines)
}

// 別のプロセス（mtb を同時に何本も動かす）でも混ざらないこと
func TestConcurrentAppendsFromProcesses(t *testing.T) {
	if os.Getenv("MTB_LOGS_HELPER_DIR") != "" {
		return
	}
	dir := t.TempDir()
	const procs, lines = 4, 200
	var cmds []*exec.Cmd
	for p := 0; p < procs; p++ {
		c := exec.Command(os.Args[0], "-test.run", "^TestHelperAppend$")
		c.Env = append(os.Environ(), "MTB_LOGS_HELPER_DIR="+dir, "MTB_LOGS_HELPER_N="+strconv.Itoa(lines), "MTB_LOGS_HELPER_ID="+strconv.Itoa(p))
		if err := c.Start(); err != nil {
			t.Fatal(err)
		}
		cmds = append(cmds, c)
	}
	for _, c := range cmds {
		if err := c.Wait(); err != nil {
			t.Fatal(err)
		}
	}
	checkLines(t, New(OSFS{}, dir, func() time.Time { return fixedNow }), procs*lines)
}

func TestHelperAppend(t *testing.T) {
	dir := os.Getenv("MTB_LOGS_HELPER_DIR")
	if dir == "" {
		t.Skip("別プロセスの書き手としてだけ動く")
	}
	n, _ := strconv.Atoi(os.Getenv("MTB_LOGS_HELPER_N"))
	l := New(OSFS{}, dir, func() time.Time { return fixedNow })
	pad := strings.Repeat("y", 2000)
	for i := 0; i < n; i++ {
		if err := l.Append(Run, []byte(fmt.Sprintf(`{"p":%s,"i":%d,"pad":%q}`, os.Getenv("MTB_LOGS_HELPER_ID"), i, pad))); err != nil {
			t.Fatal(err)
		}
	}
}

func checkLines(t *testing.T, l *Logs, want int) {
	t.Helper()
	n := 0
	if err := l.Each(Run, func(line []byte) {
		var v map[string]any
		if err := json.Unmarshal(line, &v); err != nil {
			t.Fatalf("混ざった行があります: %.80q", line)
		}
		n++
	}); err != nil {
		t.Fatal(err)
	}
	if n != want {
		t.Errorf("行数 = %d、%d のはず", n, want)
	}
}

func gz(s string) []byte {
	var b bytes.Buffer
	w := gzip.NewWriter(&b)
	_, _ = w.Write([]byte(s))
	_ = w.Close()
	return b.Bytes()
}

func TestMaintainCompressesAndDeletes(t *testing.T) {
	for _, tc := range []struct {
		name string
		fs   func(t *testing.T) (FS, string)
	}{
		{"os", func(t *testing.T) (FS, string) { return OSFS{}, t.TempDir() }},
		{"mem", func(t *testing.T) (FS, string) { return NewMemFS(), "mem:logs" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fsys, dir := tc.fs(t)
			l := New(fsys, dir, func() time.Time { return fixedNow })
			a := l.KindDir(Audit)
			put := func(name, content string) {
				if err := fsys.WriteFile(filepath.Join(a, name), []byte(content)); err != nil {
					t.Fatal(err)
				}
			}
			put(day(0)+ext, "today\n")
			put(day(1)+ext, "yesterday\n")
			put(day(2)+ext, "two\n")
			put(day(10)+ext, "ten\n")
			put(day(30)+ext, "thirty\n")
			put(day(31)+ext, "old\n")
			put(day(40)+gzExt, string(gz("older\n")))
			// 前回の圧縮が途中で止まった跡
			put(day(5)+ext, "five-full\n")
			put(day(5)+gzExt, string(gz("five-partial")))
			put(day(6)+gzExt+tmpSuffix, "garbage")

			ran, err := l.Maintain()
			if err != nil || !ran {
				t.Fatalf("Maintain = %v, %v", ran, err)
			}
			names, _ := fsys.ReadDir(a)
			got := strings.Join(names, ",")
			has := map[string]bool{}
			for _, n := range names {
				has[n] = true
			}
			for _, must := range []string{day(0) + ext, day(1) + ext, day(2) + gzExt, day(5) + gzExt, day(10) + gzExt, day(30) + gzExt} {
				if !has[must] {
					t.Errorf("%s がありません: %s", must, got)
				}
			}
			for _, gone := range []string{day(2) + ext, day(5) + ext, day(10) + ext, day(31) + ext, day(40) + gzExt, day(6) + gzExt + tmpSuffix} {
				if has[gone] {
					t.Errorf("%s が残っています: %s", gone, got)
				}
			}
			// 途中で止まった日は、.jsonl の中身で作り直している
			var lines []string
			_ = l.Each(Audit, func(b []byte) { lines = append(lines, string(b)) })
			if strings.Join(lines, "|") != "thirty|ten|five-full|two|yesterday|today" {
				t.Errorf("読めた中身 = %v", lines)
			}
			// 同じ日の2回目は動かない
			if ran, _ := l.Maintain(); ran {
				t.Error("1日に2回片付けています")
			}
		})
	}
}

func TestMaintainSkipsWhenLocked(t *testing.T) {
	dir := t.TempDir()
	l := New(OSFS{}, dir, func() time.Time { return fixedNow })
	if err := (OSFS{}).WriteFile(filepath.Join(l.KindDir(Run), day(3)+ext), []byte("x\n")); err != nil {
		t.Fatal(err)
	}
	unlock, err := (OSFS{}).TryLock(filepath.Join(dir, lockName))
	if err != nil {
		t.Fatal(err)
	}
	if ran, err := l.Maintain(); ran || err != nil {
		t.Fatalf("ほかが片付けているのに動きました: %v %v", ran, err)
	}
	if _, err := os.Stat(filepath.Join(l.KindDir(Run), day(3)+ext)); err != nil {
		t.Error("ロック中に触っています")
	}
	unlock()
	if ran, err := l.Maintain(); !ran || err != nil {
		t.Errorf("ロックが外れたあとは動くはず: %v", err)
	}
	if _, err := os.Stat(filepath.Join(l.KindDir(Run), day(3)+gzExt)); err != nil {
		t.Error("圧縮していません")
	}
}

func TestMaintainWithoutLogsIsNoop(t *testing.T) {
	l := New(OSFS{}, filepath.Join(t.TempDir(), "nothing"), func() time.Time { return fixedNow })
	if ran, err := l.Maintain(); ran || err != nil {
		t.Errorf("まだ何も無いのに動きました: %v %v", ran, err)
	}
	if _, err := os.Stat(l.Dir); err == nil {
		t.Error("ディレクトリを作っています")
	}
}

func TestRunIDIsSortableAndUnique(t *testing.T) {
	a := NewRunID(fixedNow)
	b := NewRunID(fixedNow.Add(time.Millisecond))
	c := NewRunID(fixedNow)
	if len(a) != 26 || a >= b || a == c {
		t.Errorf("ID = %s %s %s", a, b, c)
	}
	if a[:10] != c[:10] {
		t.Errorf("同じ時刻なら先頭が揃うはず: %s %s", a, c)
	}
}
