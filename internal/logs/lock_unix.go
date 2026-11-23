//go:build unix

package logs

import (
	"os"
	"syscall"
	"time"
)

// lockShared は共有ロックを wait まで待って取ります。取れたら true。
func lockShared(f *os.File, wait time.Duration) bool {
	deadline := time.Now().Add(wait)
	for {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_SH|syscall.LOCK_NB)
		if err == nil {
			return true
		}
		if err != syscall.EWOULDBLOCK || time.Now().After(deadline) {
			// flock が効かない場所（ENOLCK など）と、待ち切れなかったとき
			return false
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// tryLockExclusive は待たずに排他ロックを取ります。
// flock が効かないファイルシステムでは、取れたことにして進みます（楽観的）。
func tryLockExclusive(f *os.File) bool {
	err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	return err == nil || err != syscall.EWOULDBLOCK
}

func unlock(f *os.File) { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN) }
