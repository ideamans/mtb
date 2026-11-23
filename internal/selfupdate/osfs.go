package selfupdate

import (
	"io/fs"
	"os"
	"path/filepath"
)

// OSFS は実際のファイルを相手にする FS です。
type OSFS struct{}

func (OSFS) Stat(name string) (fs.FileMode, error) {
	st, err := os.Stat(name)
	if err != nil {
		return 0, err
	}
	return st.Mode(), nil
}

func (OSFS) WriteFile(name string, data []byte, perm fs.FileMode) error {
	f, err := os.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, perm)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

func (OSFS) Rename(oldpath, newpath string) error { return os.Rename(oldpath, newpath) }
func (OSFS) Remove(name string) error             { return os.Remove(name) }

// Executable は動いている実行ファイルの実体のパスです（シンボリックリンクは辿る）。
func Executable() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(exe)
}
