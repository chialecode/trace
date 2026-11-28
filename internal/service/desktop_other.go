//go:build !windows

package service

func openExternalURLWindows(clean string) error {
	return startAndRelease("rundll32.exe", "url.dll,FileProtocolHandler", clean)
}
