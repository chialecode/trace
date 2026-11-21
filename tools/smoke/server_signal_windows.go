//go:build windows

package main

import "time"

// terminate is not used on Windows: the smoke kills and restarts instead.
func (s *serverProc) terminate(time.Duration) bool {
	s.kill()
	return false
}
