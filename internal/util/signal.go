package util

import (
	"context"
	"os"
	"os/signal"
	"sync/atomic"
	"syscall"
)

// SignalExitCodeBase is the shell convention for "killed by signal N": 128+N.
const SignalExitCodeBase = 128

// SignalContext returns a context that is cancelled on SIGINT/SIGTERM.
//
// The returned exitCode reports 0 while nothing was cancelled and 128+signal
// afterwards, so a CLI can exit 130 for Ctrl-C and 143 for SIGTERM instead of
// pretending the scan finished normally. Call stop (usually deferred) to
// restore the default signal behaviour and release the handler goroutine.
func SignalContext() (ctx context.Context, exitCode func() int, stop func()) {
	ctx, cancel := context.WithCancel(context.Background())
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, os.Interrupt, syscall.SIGTERM)

	var received atomic.Int32
	done := make(chan struct{})
	go func() {
		defer close(done)
		select {
		case sig := <-ch:
			if s, ok := sig.(syscall.Signal); ok {
				received.Store(int32(s))
			}
			cancel()
		case <-ctx.Done():
		}
	}()

	stop = func() {
		signal.Stop(ch)
		cancel()
		<-done
	}
	exitCode = func() int {
		if s := received.Load(); s != 0 {
			return SignalExitCodeBase + int(s)
		}
		return 0
	}
	return ctx, exitCode, stop
}
