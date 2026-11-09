//go:build !windows

package process

import (
	"errors"
	"os/exec"
	"syscall"
)

type platformScope struct{}

func (*platformScope) prepare(cmd *exec.Cmd) error {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	return nil
}
func (*platformScope) attach(*exec.Cmd) error { return nil }
func (*platformScope) empty(cmd *exec.Cmd) (bool, error) {
	err := syscall.Kill(-cmd.Process.Pid, 0)
	if errors.Is(err, syscall.ESRCH) {
		return true, nil
	}
	return false, err
}
func (*platformScope) terminate(cmd *exec.Cmd) error {
	err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	if errors.Is(err, syscall.ESRCH) {
		return nil
	}
	return err
}
func (*platformScope) release() {}
