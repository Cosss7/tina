//go:build !windows

package dsh

import (
	"errors"
	"syscall"
)

func acceptanceProcessAlive(pid int) (bool, error) {
	err := syscall.Kill(pid, 0)
	if errors.Is(err, syscall.ESRCH) {
		return false, nil
	}
	return err == nil, err
}
