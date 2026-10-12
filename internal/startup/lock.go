package startup

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// Serialize modifications across CLI processes. Keep the lock file after
// releasing it so every process locks the same inode.
func lockStartup(ctx context.Context, dir string) (*os.File, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, ".shelleyctl-startup.lock")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err != nil {
		return nil, errors.Join(err, f.Close())
	}
	if !safeFile(info) {
		return nil, errors.Join(fmt.Errorf("unsafe startup lock %s", path), f.Close())
	}
	return acquireLock(ctx, f)
}

func acquireLock(ctx context.Context, f *os.File) (*os.File, error) {
	for {
		if err := ctx.Err(); err != nil {
			return nil, errors.Join(err, f.Close())
		}
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return f, nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, errors.Join(err, f.Close())
		}
		timer := time.NewTimer(25 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, errors.Join(ctx.Err(), f.Close())
		case <-timer.C:
		}
	}
}
