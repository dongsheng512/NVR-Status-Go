package recording

// cmsearchCollect 的分页补全逻辑：total > 单页时，取最后一页并与首页去重合并。
//
// 这段逻辑此前没有测试覆盖 —— 变异测试（把 return merged 改成 return matches）
// 仍然是绿的。设备在 64 路满载时 total 常超过 40，漏合并会让「最近有录像」
// 误判为「无录像」，属于会影响健康结论的静默缺陷。

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"nvrstatus/internal/model"
)

// pagingFetcher 按请求体里的 searchResultPostion 返回对应页。
type pagingFetcher struct {
	total   int
	perPage int

	mu      sync.Mutex
	offsets []int // 每次 Post 请求的 position，用于断言翻页位置
}

func (f *pagingFetcher) Get(endpoint string) (string, error) { return "", nil }

func (f *pagingFetcher) Post(endpoint, body string) (string, error) {
	pos := parsePosition(body)
	f.mu.Lock()
	f.offsets = append(f.offsets, pos)
	f.mu.Unlock()

	end := pos + f.perPage
	if end > f.total {
		end = f.total
	}
	var b strings.Builder
	b.WriteString("<CMSearchResult>")
	fmt.Fprintf(&b, "<totalMatches>%d</totalMatches>", f.total)
	b.WriteString("<searchMatchList>")
	for i := pos; i < end; i++ {
		// itemKey 取 playbackURI/startTime/endTime 三者拼接，这里造唯一值
		fmt.Fprintf(&b,
			`<searchMatchItem><playbackURI>rtsp://cam/%d</playbackURI>`+
				`<timeSpan><startTime>T%d</startTime><endTime>E%d</endTime></timeSpan>`+
				`</searchMatchItem>`, i, i, i)
	}
	b.WriteString("</searchMatchList></CMSearchResult>")
	return b.String(), nil
}

func (f *pagingFetcher) DeviceLocation() *time.Location   { return time.UTC }
func (f *pagingFetcher) Cameras() ([]model.Camera, error) { return nil, nil }
func (f *pagingFetcher) Context() context.Context         { return context.Background() }

// parsePosition 提取 <searchResultPostion>N</searchResultPostion>（设备端拼写如此）。
func parsePosition(body string) int {
	const open, close = "<searchResultPostion>", "</searchResultPostion>"
	i := strings.Index(body, open)
	if i < 0 {
		return 0
	}
	rest := body[i+len(open):]
	j := strings.Index(rest, close)
	if j < 0 {
		return 0
	}
	n, err := strconv.Atoi(strings.TrimSpace(rest[:j]))
	if err != nil {
		return 0
	}
	return n
}

func TestCMSSearchCollectMergesSecondPage(t *testing.T) {
	f := &pagingFetcher{total: 100, perPage: 40}
	s := &Service{F: f, LookbackMinutes: 60, Now: func() time.Time { return time.Unix(0, 0).UTC() }}

	start := time.Unix(0, 0).UTC()
	matches, err := s.cmsearchCollect("101", start, start.Add(time.Hour))
	if err != nil {
		t.Fatalf("cmsearchCollect: %v", err)
	}
	if len(matches) != 80 {
		t.Errorf("合并后应为 80 条（首页 40 + 末页 40），实际 %d", len(matches))
	}

	// 必须请求两次：position=0 与 position=total-perPage=60
	want := []int{0, 60}
	if len(f.offsets) != len(want) {
		t.Fatalf("应请求 %d 次，实际 %d 次: %v", len(want), len(f.offsets), f.offsets)
	}
	for i, w := range want {
		if f.offsets[i] != w {
			t.Errorf("第 %d 次请求 position=%d，期望 %d", i+1, f.offsets[i], w)
		}
	}

	// 首页与末页不能重复计入
	seen := map[string]bool{}
	for _, m := range matches {
		k := itemKey(m)
		if seen[k] {
			t.Fatalf("合并结果出现重复条目: %s", k)
		}
		seen[k] = true
	}
}

func TestCMSSearchCollectSinglePage(t *testing.T) {
	f := &pagingFetcher{total: 12, perPage: 40}
	s := &Service{F: f, LookbackMinutes: 60}

	start := time.Unix(0, 0).UTC()
	matches, err := s.cmsearchCollect("101", start, start.Add(time.Hour))
	if err != nil {
		t.Fatalf("cmsearchCollect: %v", err)
	}
	if len(matches) != 12 {
		t.Errorf("单页应返回 12 条，实际 %d", len(matches))
	}
	if len(f.offsets) != 1 {
		t.Errorf("单页不应翻页，实际请求 %d 次: %v", len(f.offsets), f.offsets)
	}
}

// 两页有重叠时必须去重。真实场景：total=45、单页 40 时，
// 首页返回 0..39，末页 position=5 返回 5..44，重叠 35 条。
func TestCMSSearchCollectDedupesOverlap(t *testing.T) {
	f := &pagingFetcher{total: 45, perPage: 40}
	s := &Service{F: f}

	start := time.Unix(0, 0).UTC()
	matches, err := s.cmsearchCollect("101", start, start.Add(time.Hour))
	if err != nil {
		t.Fatalf("cmsearchCollect: %v", err)
	}
	// 0..44 共 45 条，重叠部分不能重复计入
	if len(matches) != 45 {
		t.Errorf("去重后应为 45 条，实际 %d", len(matches))
	}
	if len(f.offsets) != 2 || f.offsets[1] != 5 {
		t.Errorf("第二次请求 position 应为 45-40=5，实际 %v", f.offsets)
	}
}
