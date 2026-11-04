package xmlnode

import "testing"

const sample = `<?xml version="1.0"?>
<ResponseStatus xmlns="http://www.hikvision.com/ver20/XMLSchema">
  <Track>
    <id>101</id>
    <Channel>101</Channel>
    <SrcChannel>1</SrcChannel>
    <Actions><Record>true</Record><ActionRecordingMode>CMR</ActionRecordingMode></Actions>
  </Track>
  <Track>
    <id>201</id>
    <Channel>201</Channel>
    <Actions><Record>true</Record></Actions>
  </Track>
</ResponseStatus>`

func TestParseFind(t *testing.T) {
	root, err := Parse(sample)
	if err != nil {
		t.Fatal(err)
	}
	if root.Name != "ResponseStatus" {
		t.Fatalf("root = %s", root.Name)
	}
	tracks := root.FindAll("Track")
	if len(tracks) != 2 {
		t.Fatalf("tracks = %d", len(tracks))
	}
	if got := tracks[0].FindText("id", ""); got != "101" {
		t.Fatalf("id = %s", got)
	}
	if got := tracks[0].FindText(".//SrcChannel", ""); got != "1" {
		t.Fatalf("src = %s", got)
	}
	if got := tracks[0].FindText(".//ActionRecordingMode", ""); got != "CMR" {
		t.Fatalf("mode = %s", got)
	}
	if got := root.FindText(".//Record", ""); got != "true" {
		t.Fatalf("record = %s", got)
	}
	if got := root.FindText("missing", "def"); got != "def" {
		t.Fatalf("missing = %s", got)
	}
}

func TestRejectsDTD(t *testing.T) {
	if _, err := Parse(`<!DOCTYPE foo [<!ENTITY x "y">]><foo/>`); err == nil {
		t.Fatal("expected error for DTD")
	}
}

func TestRejectsOversize(t *testing.T) {
	big := "<a>" + string(make([]byte, maxXMLBytes+10)) + "</a>"
	if _, err := Parse(big); err == nil {
		t.Fatal("expected error for oversize")
	}
}
