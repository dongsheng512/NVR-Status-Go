package isapi

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func mustParseXML(t *testing.T, text string) *Node {
	t.Helper()
	node, err := ParseXML([]byte(text), "test")
	if err != nil {
		t.Fatalf("ParseXML: %v", err)
	}
	return node
}

func TestNodeFindTextAndFindAll(t *testing.T) {
	root := mustParseXML(t, `<?xml version="1.0"?>
<root>
  <id>7</id>
  <nested>
    <ipAddress>10.0.0.7</ipAddress>
    <model><name>DS-2CD</name></model>
  </nested>
  <group>
    <item><value>a</value></item>
    <item><value>b</value></item>
  </group>
</root>`)

	if got := root.Tag; got != "root" {
		t.Fatalf("Tag = %q, want root", got)
	}
	if got := root.FindText("id", "def"); got != "7" {
		t.Fatalf("direct id = %q, want 7", got)
	}
	if got := root.FindText(".//ipAddress", "def"); got != "10.0.0.7" {
		t.Fatalf("descendant ipAddress = %q", got)
	}
	if got := root.FindText("nested/model/name", "def"); got != "DS-2CD" {
		t.Fatalf("path chain = %q", got)
	}
	if got := root.FindText(".//missing", "fallback"); got != "fallback" {
		t.Fatalf("missing = %q, want fallback", got)
	}
	if got := root.FindText("nested", "fallback"); got != "" {
		t.Fatalf("empty element text = %q, want empty (not default)", got)
	}

	items := root.FindAll(".//item")
	if len(items) != 2 {
		t.Fatalf("descendant items = %d, want 2", len(items))
	}
	direct := root.FindAll("group/item")
	if len(direct) != 2 {
		t.Fatalf("group/item = %d, want 2", len(direct))
	}
	if got := root.FindAll("item"); len(got) != 0 {
		t.Fatalf("direct item = %d, want 0", len(got))
	}
	if got := root.FindAll("."); len(got) != 1 || got[0] != root {
		t.Fatalf("dot path should return receiver")
	}
}

func TestParseXMLStripsNamespaces(t *testing.T) {
	root := mustParseXML(t, `<root xmlns="http://www.hikvision.com/ver20/XMLSchema" xmlns:x="urn:x">
  <x:id>7</x:id>
  <nested><ipAddress>1.2.3.4</ipAddress></nested>
</root>`)
	if root.Tag != "root" {
		t.Fatalf("root tag = %q", root.Tag)
	}
	if got := root.FindText("id", ""); got != "7" {
		t.Fatalf("namespaced direct child = %q", got)
	}
	if got := root.FindText(".//ipAddress", ""); got != "1.2.3.4" {
		t.Fatalf("namespaced descendant = %q", got)
	}
}

func TestParseXMLRejectsDTDAndEntities(t *testing.T) {
	cases := []string{
		`<?xml version="1.0"?><!DOCTYPE root [<!ENTITY xxe SYSTEM "file:///etc/passwd">]><root>&xxe;</root>`,
		`<!doctype root><root/>`,
		`<root><!ENTITY x "y"></root>`,
	}
	for _, text := range cases {
		node, err := ParseXML([]byte(text), "test")
		if node != nil {
			t.Fatalf("expected nil node for %q", text)
		}
		if !errors.Is(err, ErrDTD) {
			t.Fatalf("error = %v, want ErrDTD", err)
		}
	}
}

func TestParseXMLRejectsOversizedDocument(t *testing.T) {
	data := bytes.Repeat([]byte("a"), XMLMaxBytes+1)
	node, err := ParseXML(data, "test")
	if node != nil {
		t.Fatal("expected nil node")
	}
	if !errors.Is(err, ErrXMLTooLarge) {
		t.Fatalf("error = %v, want ErrXMLTooLarge", err)
	}
}

func TestParseXMLRejectsMalformed(t *testing.T) {
	if node, err := ParseXML([]byte(`<root><id>1</root>`), "test"); node != nil || err == nil {
		t.Fatalf("malformed XML: node=%v err=%v", node, err)
	}
	if node, err := ParseXML([]byte(`   `), "test"); node != nil || err == nil {
		t.Fatalf("empty XML: node=%v err=%v", node, err)
	}
}

func TestParseXMLTextTrimsOuterWhitespace(t *testing.T) {
	root := mustParseXML(t, "<root><name>  Front Door \n</name></root>")
	if got := root.FindText("name", ""); got != "Front Door" {
		t.Fatalf("text = %q", got)
	}
	if got := root.FindText("name", ""); strings.Contains(got, "\n") {
		t.Fatalf("text still contains newline: %q", got)
	}
}
