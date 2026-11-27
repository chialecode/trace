package companion

import (
	"math"
	"sync"
	"time"
)

// Failure limiting parameters matching docs/design/mobile.md §5.1 and remoteserver:
// 5 failures from one source IP within 1 minute block that IP for 10 minutes.
const (
	rateFailWindow    = time.Minute
	rateFailLimit     = 5
	rateBlockDuration = 10 * time.Minute
	rateMaxTrackedIPs = 10000
	ratePruneInterval = time.Minute
)

type failureLimiter struct {
	mu        sync.Mutex
	failures  map[string][]time.Time
	blocked   map[string]time.Time
	lastPrune time.Time
}

func newFailureLimiter() *failureLimiter {
	return &failureLimiter{
		failures: make(map[string][]time.Time),
		blocked:  make(map[string]time.Time),
	}
}

func (l *failureLimiter) IsBlocked(ip string, now time.Time) (bool, int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	until, ok := l.blocked[ip]
	if !ok {
		return false, 0
	}
	if now.Before(until) {
		rem := int(math.Ceil(until.Sub(now).Seconds()))
		if rem < 1 {
			rem = 1
		}
		return true, rem
	}
	delete(l.blocked, ip)
	delete(l.failures, ip)
	return false, 0
}

func (l *failureLimiter) RecordFailure(ip string, now time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.pruneLocked(now, len(l.failures) >= rateMaxTrackedIPs || len(l.blocked) >= rateMaxTrackedIPs)
	if _, exists := l.failures[ip]; !exists {
		for len(l.failures) >= rateMaxTrackedIPs || len(l.blocked) >= rateMaxTrackedIPs {
			l.evictOldestLocked()
		}
	}
	var kept []time.Time
	for _, t := range l.failures[ip] {
		if now.Sub(t) < rateFailWindow {
			kept = append(kept, t)
		}
	}
	kept = append(kept, now)
	l.failures[ip] = kept
	if len(kept) >= rateFailLimit {
		l.blocked[ip] = now.Add(rateBlockDuration)
	}
}

func (l *failureLimiter) pruneLocked(now time.Time, force bool) {
	if !force && !l.lastPrune.IsZero() && now.Sub(l.lastPrune) < ratePruneInterval {
		return
	}
	l.lastPrune = now
	for k, until := range l.blocked {
		if !now.Before(until) {
			delete(l.blocked, k)
		}
	}
	for k, times := range l.failures {
		kept := times[:0]
		for _, t := range times {
			if now.Sub(t) < rateFailWindow {
				kept = append(kept, t)
			}
		}
		if len(kept) == 0 {
			delete(l.failures, k)
		} else {
			l.failures[k] = kept
		}
	}
}

func (l *failureLimiter) evictOldestLocked() {
	var victim string
	var oldest time.Time
	for k, times := range l.failures {
		if _, isBlocked := l.blocked[k]; isBlocked {
			continue
		}
		last := times[len(times)-1]
		if victim == "" || last.Before(oldest) {
			victim, oldest = k, last
		}
	}
	if victim != "" {
		delete(l.failures, victim)
		return
	}
	for k, until := range l.blocked {
		if victim == "" || until.Before(oldest) {
			victim, oldest = k, until
		}
	}
	if victim != "" {
		delete(l.blocked, victim)
		delete(l.failures, victim)
	}
}
