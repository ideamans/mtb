// Package logs は mtb が手元に残すログ（監査ログと動作ログ）の置き場です。
//
// 置き場は設定ディレクトリの logs/（MTB_LOG_DIR で単独に移せる）で、
// 種類ごとのディレクトリに UTC の日付ごとの JSON Lines を書きます。
//
//	logs/
//	├── .gitignore            中身は "*"（dotfiles で ~/.config を管理していても混ざらない）
//	├── audit/2026-11-23.jsonl  監査ログ（mtb history）
//	├── run/2026-11-23.jsonl    動作ログ（mtb logs）
//	├── .maintenance.lock
//	└── .maintenance-stamp
//
// 2日以上前のファイルは gzip で圧縮し、30日より前のものは消します。
// 片付けは常駐せず、mtb が起動したときに1日1回だけ行います。
//
// **ログのために本来の作業を止めない。** 書けなくても呼び出し側は警告を
// 1回出すだけにする約束で、この package もエラーを返すだけで何も止めません。
package logs

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"errors"
	"io"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Kind はログの種類です。ディレクトリ名になります。
type Kind string

const (
	// Audit は監査ログ（Data API の呼び出し1回で1行）です。
	Audit Kind = "audit"
	// Run は動作ログ（出来事1つで1行）です。
	Run Kind = "run"
)

// Policy は圧縮と保存の決まりです。
//
// いまは設定の口を設けていません（利用者が日数を変える場面がまだ無い）。
// 変えられるようにするときは、ここを環境変数か設定ファイルから埋めます。
var Policy = struct {
	// CompressAfterDays 日以上前のファイルを圧縮します。昨日の分は残すため 2。
	// 日付が変わる直前に始まった書き込みが、昨日のファイルにまだ追記しているかもしれない。
	CompressAfterDays int
	// RetentionDays 日より前のファイルを消します。0 なら消しません。
	RetentionDays int
}{CompressAfterDays: 2, RetentionDays: 30}

const (
	ext        = ".jsonl"
	gzExt      = ".jsonl.gz"
	tmpSuffix  = ".tmp"
	dateLayout = "2006-01-02"
	lockName   = ".maintenance.lock"
	stampName  = ".maintenance-stamp"
	ignoreName = ".gitignore"
)

// Logs は1つのログディレクトリです。
type Logs struct {
	FS  FS
	Dir string
	Now func() time.Time

	ignoreOnce sync.Once
}

// New は Logs を作ります。now が nil なら time.Now です。
func New(fsys FS, dir string, now func() time.Time) *Logs {
	if now == nil {
		now = time.Now
	}
	return &Logs{FS: fsys, Dir: dir, Now: now}
}

// KindDir は種類ごとのディレクトリです。
func (l *Logs) KindDir(k Kind) string { return filepath.Join(l.Dir, string(k)) }

// FileFor は t の時点で書くファイルです（UTC の日付）。
func (l *Logs) FileFor(k Kind, t time.Time) string {
	return filepath.Join(l.KindDir(k), t.UTC().Format(dateLayout)+ext)
}

// Append は1行を足します。line の末尾に改行が無ければ足します。
// 1行は1回の書き込みで送るので、並んで動く mtb どうしでも行は混ざりません。
func (l *Logs) Append(k Kind, line []byte) error {
	if len(line) == 0 || line[len(line)-1] != '\n' {
		line = append(append([]byte{}, line...), '\n')
	}
	if err := l.FS.Append(l.FileFor(k, l.Now()), line); err != nil {
		return err
	}
	l.ignoreOnce.Do(l.ensureIgnore)
	return nil
}

// ensureIgnore はログのディレクトリに .gitignore を置きます（無ければ）。
// 置けなくても困らないので、失敗は捨てます。
func (l *Logs) ensureIgnore() {
	name := filepath.Join(l.Dir, ignoreName)
	if _, err := l.FS.ReadFile(name); errors.Is(err, fs.ErrNotExist) {
		_ = l.FS.WriteFile(name, []byte("# mtb のログ（mtb history / mtb logs）。git に入れない\n*\n"))
	}
}

// file はディレクトリの中の1ファイルです。
type file struct {
	name string // ディレクトリからの名前
	date time.Time
	gz   bool
}

// files は種類のディレクトリにあるログを、日付の古い順に返します。
// 同じ日付の .jsonl と .jsonl.gz が両方あれば .jsonl を先に並べます。
func (l *Logs) files(k Kind) ([]file, []string, error) {
	names, err := l.FS.ReadDir(l.KindDir(k))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil, nil
		}
		return nil, nil, err
	}
	var out []file
	var other []string
	for _, n := range names {
		var stem string
		gz := false
		switch {
		case strings.HasSuffix(n, gzExt):
			stem, gz = strings.TrimSuffix(n, gzExt), true
		case strings.HasSuffix(n, ext):
			stem = strings.TrimSuffix(n, ext)
		default:
			other = append(other, n)
			continue
		}
		d, err := time.Parse(dateLayout, stem)
		if err != nil {
			other = append(other, n)
			continue
		}
		out = append(out, file{name: n, date: d, gz: gz})
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].date.Equal(out[j].date) {
			return out[i].date.Before(out[j].date)
		}
		return !out[i].gz && out[j].gz
	})
	return out, other, nil
}

// Each は種類のログを古い日付から1行ずつ渡します（.jsonl.gz も読みます）。
// 同じ日付の .jsonl と .jsonl.gz が両方あるのは圧縮の途中で止まったときで、
// .jsonl のほうが中身を全部持っているので、そちらだけを読みます。
func (l *Logs) Each(k Kind, fn func(line []byte)) error {
	fs, _, err := l.files(k)
	if err != nil {
		return err
	}
	var last time.Time
	for _, f := range fs {
		if f.date.Equal(last) {
			continue // 同じ日付の2つ目（.gz）
		}
		last = f.date
		if err := l.EachInFile(filepath.Join(l.KindDir(k), f.name), fn); err != nil {
			return err
		}
	}
	return nil
}

// EachInFile は1ファイルを1行ずつ渡します。名前が .gz で終われば展開して読みます。
func (l *Logs) EachInFile(name string, fn func(line []byte)) error {
	buf, err := l.FS.ReadFile(name)
	if err != nil {
		return err
	}
	var r io.Reader = bytes.NewReader(buf)
	if strings.HasSuffix(name, ".gz") {
		zr, err := gzip.NewReader(r)
		if err != nil {
			return err
		}
		defer zr.Close()
		r = zr
	}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 16*1024*1024)
	for sc.Scan() {
		if line := bytes.TrimSpace(sc.Bytes()); len(line) > 0 {
			fn(line)
		}
	}
	// 圧縮の途中で切れた .gz の末尾などは、読めたところまでで良しとする
	if err := sc.Err(); err != nil && !errors.Is(err, io.ErrUnexpectedEOF) {
		return err
	}
	return nil
}

// Maintain は古いログを圧縮し、期限を過ぎたものを消します。
//
// 1日1回だけ動きます（.maintenance-stamp に日付を残す）。ほかの mtb が
// 片付けている最中（.maintenance.lock が取れない）なら何もしません。
// 動いたら true を返します。
func (l *Logs) Maintain() (bool, error) {
	today := l.Now().UTC().Format(dateLayout)
	stamp := filepath.Join(l.Dir, stampName)
	if b, err := l.FS.ReadFile(stamp); err == nil && strings.TrimSpace(string(b)) == today {
		return false, nil
	}
	if _, err := l.FS.ReadDir(l.Dir); err != nil {
		// まだ1行も書いていない。片付けるものが無い
		return false, nil
	}
	unlock, err := l.FS.TryLock(filepath.Join(l.Dir, lockName))
	if err != nil {
		if errors.Is(err, ErrLocked) {
			return false, nil
		}
		return false, err
	}
	defer unlock()
	// 鍵を待つ間に、ほかの mtb が片付け終えたかもしれない
	if b, err := l.FS.ReadFile(stamp); err == nil && strings.TrimSpace(string(b)) == today {
		return false, nil
	}

	day, _ := time.Parse(dateLayout, today)
	var errs []error
	for _, k := range []Kind{Audit, Run} {
		if err := l.maintainKind(k, day); err != nil {
			errs = append(errs, err)
		}
	}
	if err := l.FS.WriteFile(stamp, []byte(today+"\n")); err != nil {
		errs = append(errs, err)
	}
	return true, errors.Join(errs...)
}

func (l *Logs) maintainKind(k Kind, today time.Time) error {
	files, other, err := l.files(k)
	if err != nil {
		return err
	}
	dir := l.KindDir(k)
	var errs []error
	// 圧縮の途中で落ちたときの残り
	for _, n := range other {
		if strings.HasSuffix(n, tmpSuffix) {
			if err := l.FS.Remove(filepath.Join(dir, n)); err != nil {
				errs = append(errs, err)
			}
		}
	}
	for _, f := range files {
		age := int(today.Sub(f.date).Hours() / 24)
		path := filepath.Join(dir, f.name)
		switch {
		case Policy.RetentionDays > 0 && age > Policy.RetentionDays:
			if err := l.FS.Remove(path); err != nil {
				errs = append(errs, err)
			}
		case !f.gz && age >= Policy.CompressAfterDays:
			// .gz が既にあっても作り直す（前回が途中で止まった。.jsonl のほうが全部を持つ）
			dst := strings.TrimSuffix(path, ext) + gzExt
			if err := l.FS.Compress(path, dst); err != nil && !errors.Is(err, ErrLocked) {
				errs = append(errs, err)
			}
		}
	}
	return errors.Join(errs...)
}
