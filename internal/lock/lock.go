// Package lock は、割り当てられた slot に対する「同時実行の排他」を flock で行う。
//
// store の割り当てマッピング（永続）とは別レイヤー。store は「どの key がどの番号か」を
// 覚えるだけで、同じ (scope, key) が同時に2本走るのを止めるのは flock の役目。
// flock はプロセス消滅で自動解放されるため、クラッシュしても stale lock が残らない。
package lock

import (
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// Lock は保持中の flock を表す。Release でファイルを閉じて解放する。
type Lock struct {
	f *os.File
}

// ErrBusy は、ロックが他プロセスに保持されていて取得できなかった場合に返す。
var ErrBusy = fmt.Errorf("lock busy")

// TryAcquire は dir 配下に name.lock を開き、非ブロッキングで排他ロックを試みる。
// 既に他プロセスが保持していれば ErrBusy を返す。
func TryAcquire(dir, name string) (*Lock, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create lock dir: %w", err)
	}
	path := filepath.Join(dir, name+".lock")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open lock file: %w", err)
	}
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		f.Close()
		if err == unix.EWOULDBLOCK {
			return nil, ErrBusy
		}
		return nil, fmt.Errorf("flock: %w", err)
	}
	return &Lock{f: f}, nil
}

// Release はロックを解放する（ファイルクローズで flock も解除される）。
func (l *Lock) Release() error {
	if l == nil || l.f == nil {
		return nil
	}
	err := l.f.Close()
	l.f = nil
	return err
}
