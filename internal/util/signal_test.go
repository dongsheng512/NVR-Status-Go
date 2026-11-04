package util

import (
	"os"
	"runtime"
	"syscall"
	"testing"
	"time"
)

// sendSelfSignal delivers sig to the current process. The handler installed by
// SignalContext must already be active, otherwise the test binary dies.
func sendSelfSignal(t *testing.T, sig os.Signal) {
	t.Helper()
	p, err := os.FindProcess(os.Getpid())
	if err != nil {
		t.Fatalf("FindProcess: %v", err)
	}
	if err := p.Signal(sig); err != nil {
		t.Skipf("本平台不支持向自身发送 %v: %v", sig, err)
	}
}

func TestSignalContextReportsSignalExitCode(t *testing.T) {
	cases := []struct {
		name string
		sig  os.Signal
		want int
	}{
		{"Interrupt", os.Interrupt, 130},
		{"SIGTERM", syscall.SIGTERM, 143},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if runtime.GOOS == "windows" {
				t.Skip("Windows 只能可靠投递 os.Interrupt")
			}
			ctx, exitCode, stop := SignalContext()
			defer stop()

			if got := exitCode(); got != 0 {
				t.Fatalf("未收到信号时 exitCode = %d, 期望 0", got)
			}
			select {
			case <-ctx.Done():
				t.Fatal("尚未收到信号，ctx 却已被取消")
			default:
			}

			sendSelfSignal(t, tc.sig)
			select {
			case <-ctx.Done():
			case <-time.After(5 * time.Second):
				t.Fatal("信号已发送但 ctx 未被取消")
			}
			if got := exitCode(); got != tc.want {
				t.Fatalf("exitCode = %d, 期望 %d", got, tc.want)
			}
		})
	}
}

func TestSignalContextStopIsIdempotent(t *testing.T) {
	_, exitCode, stop := SignalContext()
	stop()
	stop()
	if got := exitCode(); got != 0 {
		t.Fatalf("未收到信号时 exitCode = %d, 期望 0", got)
	}
}
