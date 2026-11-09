//go:build !windows

package filelock

import (
	"os"
	"syscall"
)

func acquire(file *os.File) error {
	return syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
}
