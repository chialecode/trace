//go:build !windows

package companion

func newPlatformSleepInhibitor() SleepInhibitor {
	return noopSleepInhibitor{}
}
