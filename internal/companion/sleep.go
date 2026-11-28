package companion

// SleepInhibitor prevents system idle sleep while companion.preventSleep is
// enabled AND at least one mobile client is actively connected (FR-MOB-030).
// Tests inject a fake implementation so OS power state is never touched in tests.
type SleepInhibitor interface {
	SetActive(active bool) error
	Close() error
}

type noopSleepInhibitor struct{}

func (noopSleepInhibitor) SetActive(bool) error { return nil }
func (noopSleepInhibitor) Close() error         { return nil }
