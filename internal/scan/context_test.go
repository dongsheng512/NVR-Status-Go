package scan

// ctx 的安装点必须同时覆盖 ISAPI 客户端和抽检层。
//
// 这里锁住的是一次真实回归：Build 里曾写成 `Ctx: client.Context()`，而 ctx 是在
// 之后的 onNVR 钩子里才 SetContext 的 —— 快照拿到的是 Background。结果是
// HTTP 请求能取消、ffmpeg 子进程不能，用户点了取消还要等整路拉流自己结束。

import (
	"context"
	"testing"

	"nvrstatus/internal/model"
	"nvrstatus/internal/util"
)

func buildNVR(t *testing.T) *NVR {
	t.Helper()
	r := NewRunner(Options{Quiet: true})
	n := r.Build(model.Device{IP: "192.0.2.1", Username: "admin", Password: "pw"}, model.DefaultScanOptions())
	if n.AV == nil {
		t.Fatal("AV 层未构建")
	}
	return n
}

func TestSetContextReachesAVLayer(t *testing.T) {
	n := buildNVR(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	n.SetContext(ctx)

	if n.AV.Ctx != ctx {
		t.Fatal("SetContext 没有把 ctx 传到 AV 层：取消后 ffmpeg 子进程不会被杀掉")
	}

	cancel()
	if err := n.AV.Run(nil); err != util.ErrCancelled {
		t.Fatalf("取消后 AV.Run = %v, 期望 ErrCancelled", err)
	}
}

// bindContext 是 RunWith/QueueCtx 实际使用的钩子。
func TestBindContextInstallsOnBothLayers(t *testing.T) {
	n := buildNVR(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	hook := bindContext(ctx)
	if hook == nil {
		t.Fatal("bindContext(non-nil) 返回了 nil")
	}
	hook(n)

	if got := n.Client.Context(); got != ctx {
		t.Error("客户端没有拿到 ctx：HTTP 请求无法被取消")
	}
	if n.AV.Ctx != ctx {
		t.Error("AV 层没有拿到 ctx：ffmpeg 子进程无法被取消")
	}
}

func TestBindContextNilMeansBackground(t *testing.T) {
	if hook := bindContext(nil); hook != nil {
		t.Fatal("bindContext(nil) 应返回 nil，表示不改变默认行为")
	}
}

// 没有安装 ctx 时，AV 层照常工作（Background），不能因为 nil 而 panic。
func TestSetContextNilIsSafe(t *testing.T) {
	n := buildNVR(t)
	n.SetContext(context.Background())
	if err := n.AV.Run(nil); err == util.ErrCancelled {
		t.Fatal("未取消时不应返回 ErrCancelled")
	}
}
