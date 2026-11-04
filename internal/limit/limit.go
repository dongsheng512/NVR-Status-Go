// Package limit provides a small counting semaphore used to keep concurrent
// device work and ffmpeg processes within a safe global budget.
package limit

import "time"

// Limiter is a counting semaphore. A nil Limiter means "no limit".
type Limiter struct {
	ch chan struct{}
}

// New returns a limiter allowing n concurrent holders.
func New(n int) *Limiter {
	if n < 1 {
		n = 1
	}
	return &Limiter{ch: make(chan struct{}, n)}
}

// Acquire waits for a slot. It polls check so a canceled scan does not block
// forever behind a held slot.
func (l *Limiter) Acquire(check func() error) error {
	if l == nil {
		return nil
	}
	for {
		select {
		case l.ch <- struct{}{}:
			return nil
		default:
			if check != nil {
				if err := check(); err != nil {
					return err
				}
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
}

// Release returns a slot. It is safe to call for a limiter that was not
// acquired (for example when a nil limiter was used).
func (l *Limiter) Release() {
	if l == nil {
		return
	}
	select {
	case <-l.ch:
	default:
	}
}
