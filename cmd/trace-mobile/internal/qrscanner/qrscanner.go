// Package qrscanner provides Android live camera QR code scanning and the
// loopback-only network security configuration for the companion app.
package qrscanner

import "github.com/egoist/mygo/native"

// Scanner displays a back-camera preview and emits decoded QR payloads.
//
//mygo:native
type Scanner interface {
	native.View
	// Start begins camera preview and QR analysis.
	Start(onScanned func(payload string), onError func(msg string))
	// Stop pauses camera preview and releases the camera binding.
	Stop()
	// Close stops the scanner and releases resources.
	Close() error
}
