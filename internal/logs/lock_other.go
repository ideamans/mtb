//go:build !unix

package logs

import (
	"os"
	"time"
)

// Windows などではロックを効かせず、楽観的に書きます。
// 1行は1回の書き込みで足すので、混ざることはまずありません。
// 片付けが2つ同時に走っても、同じ結果を作り直すだけで壊れません。

func lockShared(*os.File, time.Duration) bool { return false }

func tryLockExclusive(*os.File) bool { return true }

func unlock(*os.File) {}
