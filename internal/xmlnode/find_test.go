package xmlnode

import (
	"strings"
	"testing"
)

// Find 走的是提前返回的路径，FindAll 是完整遍历。两者对同一路径必须给出一致
// 的「第一个命中」。
func TestFindMatchesFindAllFirst(t *testing.T) {
	paths := []string{
		".//target",
		".//wrap/target",
		"direct",
		"wrap/target",
		".//missing",
		".//wrap/missing",
	}
	root, err := Parse(`<root>
		<direct>d</direct>
		<wrap><target>t1</target></wrap>
		<wrap><target>t2</target></wrap>
	</root>`)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range paths {
		want := root.FindAll(p)
		got := root.Find(p)
		switch {
		case len(want) == 0 && got != nil:
			t.Errorf("%s: Find 返回节点，FindAll 无结果", p)
		case len(want) > 0 && got != want[0]:
			t.Errorf("%s: Find 返回 %v，FindAll[0] 是 %v", p, got, want[0])
		}
	}
}

// 关键陷阱：".//A/B" 不能简化为「第一个 A 的 B」。若第一个 A 没有 B，
// FindAll 会落到第二个 A 上。
func TestFindFallsThroughWhenFirstMatchLacksChild(t *testing.T) {
	root, err := Parse(`<r><item><other>1</other></item><item><want>2</want></item></r>`)
	if err != nil {
		t.Fatal(err)
	}
	got := root.Find(".//item/want")
	if got == nil || got.Text != "2" {
		t.Fatalf(".//item/want = %v, 期望落到第二个 item 的 want", got)
	}
}

// 深层嵌套：遍历必须用显式栈而不是递归。maxXMLBytes 允许 8 MiB 文档，
// 递归版在这里会吃掉几十 MB 栈。
func TestDeeplyNestedDocument(t *testing.T) {
	const depth = 50000
	var b strings.Builder
	b.WriteString(strings.Repeat("<a>", depth))
	b.WriteString("<target>deep</target>")
	b.WriteString(strings.Repeat("</a>", depth))

	root, err := Parse(b.String())
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got := root.FindAll(".//target"); len(got) != 1 || got[0].Text != "deep" {
		t.Fatalf("FindAll(.//target) = %v，期望命中深层节点", got)
	}
	if got := root.Find(".//target"); got == nil || got.Text != "deep" {
		t.Fatalf("Find(.//target) = %v，期望命中深层节点", got)
	}
	if got := root.FindText(".//target", ""); got != "deep" {
		t.Fatalf("FindText = %q", got)
	}
}

// 截断文档必须报错，不能被当成「解析成功但内容为空」。
func TestTruncatedDocumentIsAnError(t *testing.T) {
	for _, in := range []string{"<a><b>1</b>", "<a>text", "<a><b>1</b><c>2"} {
		if _, err := Parse(in); err == nil {
			t.Errorf("Parse(%q) 应报错", in)
		}
	}
}
