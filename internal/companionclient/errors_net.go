package companionclient

import (
	"errors"
	"strings"
	"syscall"
)

func isConnRefused(err error, lower string) bool {
	if errors.Is(err, syscall.ECONNREFUSED) {
		return true
	}
	return strings.Contains(lower, "connection refused") ||
		strings.Contains(lower, "actively refused")
}
