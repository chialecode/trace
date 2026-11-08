//go:build !windows

package main

import "errors"

// The rehearsal installs the Windows package; elsewhere it does not run
// and the release stops: releases are built on Windows.
func runRehearsal(in rehearsalInput) (rehearsalResult, error) {
	res := newRehearsalResult(in)
	res.Status, res.Reason = "not-run", "the installer rehearsal needs Windows"
	return res, errors.New(res.Reason)
}
