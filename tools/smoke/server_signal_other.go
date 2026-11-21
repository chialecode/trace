//go:build !windows

package main

import (
	"syscall"
	"time"
)

// terminate sends SIGTERM and waits for the process to exit.
func (s *serverProc) terminate(wait time.Duration) bool {
	if err := s.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		return false
	}
	select {
	case <-s.done:
		return s.cmd.ProcessState != nil && s.cmd.ProcessState.Success()
	case <-time.After(wait):
		s.kill()
		return false
	}
}
