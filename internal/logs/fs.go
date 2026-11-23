package logs

import (
	"bytes"
	"compress/gzip"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// ErrLocked はロックがほかに取られていることを表します。
var ErrLocked = errors.New("locked")

// FS はログの読み書きです。実物は OSFS、テストは MemFS を使います。
type FS interface {
	// Append はファイルの末尾に data を1回の書き込みで足します（無ければ作る）。
	// 圧縮とぶつからないよう共有ロックを取りますが、取れなければ少し待って
	// ロックなしで書きます（楽観的。記録を落とさないことを優先する）。
	Append(name string, data []byte) error
	ReadFile(name string) ([]byte, error)
	WriteFile(name string, data []byte) error
	// ReadDir はディレクトリ直下のファイル名を返します。無ければ fs.ErrNotExist。
	ReadDir(dir string) ([]string, error)
	Remove(name string) error
	// Compress は src を gzip にして dst へ置き、src を消します。
	// src に排他ロックが取れなければ ErrLocked を返し、何もしません。
	Compress(src, dst string) error
	// TryLock は待たずに排他ロックを取ります。取れなければ ErrLocked。
	TryLock(name string) (unlock func(), err error)
}

// appendLockWait は追記のときに共有ロックを待つ長さです。
const appendLockWait = 200 * time.Millisecond

// OSFS は実際のファイルシステムです。
//
// ログには送った値が残るので、ディレクトリは 0700、ファイルは 0600 で作ります。
// ロックは flock（Windows では効かせず、楽観的に書く）。NFS のように flock が
// 効かない場所でも、ロックを諦めて書くだけで止まりません。
type OSFS struct{}

func (OSFS) Append(name string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(name), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(name, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	locked := lockShared(f, appendLockWait)
	_, werr := f.Write(data)
	if locked {
		unlock(f)
	}
	cerr := f.Close()
	if werr != nil {
		return werr
	}
	return cerr
}

func (OSFS) ReadFile(name string) ([]byte, error) { return os.ReadFile(name) }

func (OSFS) WriteFile(name string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(name), 0o700); err != nil {
		return err
	}
	return os.WriteFile(name, data, 0o600)
}

func (OSFS) ReadDir(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() {
			out = append(out, e.Name())
		}
	}
	return out, nil
}

func (OSFS) Remove(name string) error {
	err := os.Remove(name)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}

func (OSFS) Compress(src, dst string) error {
	f, err := os.Open(src)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil // ほかの mtb が先に片付けた
		}
		return err
	}
	defer f.Close()
	if !tryLockExclusive(f) {
		return ErrLocked
	}
	defer unlock(f)

	var buf bytes.Buffer
	if _, err := buf.ReadFrom(f); err != nil {
		return err
	}
	tmp := dst + tmpSuffix
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	zw := gzip.NewWriter(out)
	_, werr := zw.Write(buf.Bytes())
	if err := errors.Join(werr, zw.Close(), out.Sync(), out.Close()); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, dst); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Remove(src)
}

func (OSFS) TryLock(name string) (func(), error) {
	if err := os.MkdirAll(filepath.Dir(name), 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(name, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if !tryLockExclusive(f) {
		_ = f.Close()
		return nil, ErrLocked
	}
	return func() {
		unlock(f)
		_ = f.Close()
	}, nil
}

// MemFS はメモリの上の FS です（テスト用）。
type MemFS struct {
	mu    sync.Mutex
	Files map[string][]byte
	held  map[string]bool
	// FailAppend が非 nil なら Append はそれを返します（書けないときの確認用）。
	FailAppend error
}

// NewMemFS は空の MemFS を作ります。
func NewMemFS() *MemFS { return &MemFS{Files: map[string][]byte{}, held: map[string]bool{}} }

func (m *MemFS) Append(name string, data []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.FailAppend != nil {
		return m.FailAppend
	}
	m.Files[name] = append(m.Files[name], data...)
	return nil
}

func (m *MemFS) ReadFile(name string) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	b, ok := m.Files[name]
	if !ok {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
	}
	return append([]byte{}, b...), nil
}

func (m *MemFS) WriteFile(name string, data []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Files[name] = append([]byte{}, data...)
	return nil
}

func (m *MemFS) ReadDir(dir string) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	prefix := strings.TrimSuffix(dir, "/") + "/"
	found := false
	var out []string
	for name := range m.Files {
		if !strings.HasPrefix(name, prefix) {
			continue
		}
		found = true
		if rest := strings.TrimPrefix(name, prefix); !strings.Contains(rest, "/") {
			out = append(out, rest)
		}
	}
	if !found {
		return nil, &fs.PathError{Op: "readdir", Path: dir, Err: fs.ErrNotExist}
	}
	sort.Strings(out)
	return out, nil
}

func (m *MemFS) Remove(name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.Files, name)
	return nil
}

func (m *MemFS) Compress(src, dst string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.held[src] {
		return ErrLocked
	}
	b, ok := m.Files[src]
	if !ok {
		return nil
	}
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	_, _ = zw.Write(b)
	_ = zw.Close()
	m.Files[dst] = buf.Bytes()
	delete(m.Files, src)
	return nil
}

func (m *MemFS) TryLock(name string) (func(), error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.held[name] {
		return nil, ErrLocked
	}
	m.held[name] = true
	return func() {
		m.mu.Lock()
		delete(m.held, name)
		m.mu.Unlock()
	}, nil
}

// Hold はロックを取ったままにします（ほかの mtb が片付けている状態の再現）。
func (m *MemFS) Hold(name string) func() {
	u, err := m.TryLock(name)
	if err != nil {
		return func() {}
	}
	return u
}
