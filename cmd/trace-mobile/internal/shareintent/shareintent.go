// Package shareintent bridges Android ACTION_SEND (text/plain) share intents
// from other apps into TRACE Mobile (FR-MOB-030). On non-Android platforms,
// NewReceiver returns errors.ErrUnsupported so desktop builds and tests need no cgo.
package shareintent

// Receiver listens for Android ACTION_SEND plain-text share intents directed at TRACE.
//
//mygo:native
type Receiver interface {
	// Start begins listening for ACTION_SEND intents and delivers any pending
	// cold-start share text once to onShared.
	Start(onShared func(text string))
	// Stop stops listening for share intents.
	Stop()
	// Close releases resources held by the receiver.
	Close() error
}
