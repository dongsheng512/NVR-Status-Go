package xmlnode

import (
	"fmt"
	"strings"
	"testing"
)

// cmsearchSample 造一个形状接近真实 CMSearch 响应的文档：
// n 条 matchItem，每条 3 个被读取的字段。
func cmsearchSample(n int) string {
	var b strings.Builder
	b.WriteString(`<CMSearchResult><totalMatches>`)
	fmt.Fprintf(&b, "%d", n)
	b.WriteString(`</totalMatches><searchMatchList>`)
	for i := 0; i < n; i++ {
		fmt.Fprintf(&b,
			`<searchMatchItem><sourceID>cam%d</sourceID>`+
				`<playbackURI>rtsp://192.0.2.11:554/Streaming/tracks/%d01/?starttime=20260101T000000Z</playbackURI>`+
				`<timeSpan><startTime>2026-01-01T00:00:%02dZ</startTime><endTime>2026-01-01T00:01:%02dZ</endTime></timeSpan>`+
				`<mediaSegmentDescriptor><contentType>video</contentType></mediaSegmentDescriptor>`+
				`</searchMatchItem>`, i, i, i%60, i%60)
	}
	b.WriteString(`</searchMatchList></CMSearchResult>`)
	return b.String()
}

var benchDoc = cmsearchSample(40)

// 解析一次的成本（每页一次）。
func BenchmarkParse(b *testing.B) {
	for i := 0; i < b.N; i++ {
		if _, err := Parse(benchDoc); err != nil {
			b.Fatal(err)
		}
	}
}

// 取全部 matchItem（每页一次）。
func BenchmarkFindAll(b *testing.B) {
	root, err := Parse(benchDoc)
	if err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if got := root.FindAll(".//searchMatchItem"); len(got) != 40 {
			b.Fatalf("got %d", len(got))
		}
	}
}

// 逐条取 3 个字段（每页 120 次，这才是真正的热路径）。
func BenchmarkFindTextPerItem(b *testing.B) {
	root, err := Parse(benchDoc)
	if err != nil {
		b.Fatal(err)
	}
	items := root.FindAll(".//searchMatchItem")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for _, it := range items {
			_ = it.FindText(".//playbackURI", "")
			_ = it.FindText(".//startTime", "")
			_ = it.FindText(".//endTime", "")
		}
	}
}

// Find 只需第一个命中；目标在文档开头时最能体现提前返回的收益。
func BenchmarkFindFirst(b *testing.B) {
	root, err := Parse(benchDoc)
	if err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if root.Find(".//searchMatchItem") == nil {
			b.Fatal("not found")
		}
	}
}
