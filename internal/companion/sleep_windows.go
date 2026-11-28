//go:build windows

package companion

import (
	"fmt"
	"os"
	"runtime"
	"sync"

	"golang.org/x/sys/windows"
)

const (
	esSystemRequired = 0x00000001
	esContinuous     = 0x80000000
)

var (
	modKernel32Power         = windows.NewLazySystemDLL("kernel32.dll")
	procSetThreadExecutionSt = modKernel32Power.NewProc("SetThreadExecutionState")
)

type sleepCmd struct {
	active bool
	close  bool
	reply  chan error
}

// windowsSleepInhibitor calls SetThreadExecutionState on a lazily started
// OS-locked goroutine because thread execution state is thread-affine on Windows.
// Using ES_CONTINUOUS | ES_SYSTEM_REQUIRED keeps the system awake while still
// allowing the display to turn off.
type windowsSleepInhibitor struct {
	mu     sync.Mutex
	cmdCh  chan sleepCmd
	doneCh chan struct{}
	closed bool
}

func newPlatformSleepInhibitor() SleepInhibitor {
	// Never alter host power state during quiet/smoke runs.
	if os.Getenv("TRACE_QUIET") != "" {
		return noopSleepInhibitor{}
	}
	return &windowsSleepInhibitor{}
}

func (w *windowsSleepInhibitor) ensureWorkerLocked() {
	if w.cmdCh != nil {
		return
	}
	cmdCh := make(chan sleepCmd)
	doneCh := make(chan struct{})
	w.cmdCh = cmdCh
	w.doneCh = doneCh

	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		defer close(doneCh)

		active := false
		for cmd := range cmdCh {
			if cmd.close {
				if active {
					_, _, _ = procSetThreadExecutionSt.Call(uintptr(esContinuous))
					active = false
				}
				if cmd.reply != nil {
					cmd.reply <- nil
				}
				return
			}
			var flags uintptr = esContinuous
			if cmd.active {
				flags |= esSystemRequired
			}
			r1, _, callErr := procSetThreadExecutionSt.Call(flags)
			if r1 == 0 {
				if cmd.reply != nil {
					cmd.reply <- fmt.Errorf("SetThreadExecutionState failed: %w", callErr)
				}
				continue
			}
			active = cmd.active
			if cmd.reply != nil {
				cmd.reply <- nil
			}
		}
		if active {
			_, _, _ = procSetThreadExecutionSt.Call(uintptr(esContinuous))
		}
	}()
}

func (w *windowsSleepInhibitor) SetActive(active bool) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return nil
	}
	// Do not spawn the OS thread until sleep inhibition is actually requested.
	if !active && w.cmdCh == nil {
		return nil
	}
	w.ensureWorkerLocked()
	reply := make(chan error, 1)
	w.cmdCh <- sleepCmd{active: active, reply: reply}
	return <-reply
}

func (w *windowsSleepInhibitor) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return nil
	}
	w.closed = true
	if w.cmdCh == nil {
		return nil
	}
	reply := make(chan error, 1)
	w.cmdCh <- sleepCmd{close: true, reply: reply}
	err := <-reply
	close(w.cmdCh)
	<-w.doneCh
	w.cmdCh = nil
	w.doneCh = nil
	return err
}
