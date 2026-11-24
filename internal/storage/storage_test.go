package storage

import (
	"context"
	"strings"
	"testing"

	"nvrstatus/internal/model"
)

type fakeFetcher struct {
	get map[string]string
}

func (f *fakeFetcher) Get(endpoint string) (string, error) { return f.get[endpoint], nil }
func (f *fakeFetcher) Context() context.Context            { return context.Background() }

const storageXML = `<Storage>
 <hdd>
  <hddName>hdd1</hddName><capacity>10485760</capacity><freeSpace>5242880</freeSpace>
  <status>ok</status><hddType>SATA</hddType><property>RW</property><manufacturer>WD</manufacturer>
 </hdd>
 <hdd>
  <hddName>hdd2</hddName><capacity>10485760</capacity>
  <status>ok</status><hddType>SATA</hddType><property>RW</property><manufacturer>WD</manufacturer>
 </hdd>
</Storage>`

func TestStorageStatus(t *testing.T) {
	f := &fakeFetcher{get: map[string]string{"/ContentMgmt/storage": storageXML}}
	s := &Service{F: f}
	drives, err := s.StorageStatus()
	if err != nil {
		t.Fatal(err)
	}
	if len(drives) != 2 {
		t.Fatalf("drives = %d", len(drives))
	}
	if drives[0].CapacityTB != "10.0" || drives[0].UsedTB != "5.0" || drives[0].FreeTB != "5.0" {
		t.Fatalf("drive0 = %+v", drives[0])
	}
	if drives[0].Usage != "50.0%" {
		t.Fatalf("usage = %s", drives[0].Usage)
	}
	if drives[1].Usage != "未知" || drives[1].UsedTB != "未知" {
		t.Fatalf("drive1 = %+v", drives[1])
	}
}

func TestParseStrategy(t *testing.T) {
	cases := map[string]bool{
		"覆盖":        true,
		"循环覆盖":      true,
		"不覆盖":       false,
		"停止":        false,
		"overwrite": true,
		"stop":      false,
		"enable":    true,
		"disable":   false,
	}
	for in, want := range cases {
		got, ok := parseStrategy(in)
		if !ok || got != want {
			t.Errorf("%q: got %v/%v want %v", in, got, ok, want)
		}
	}
}

func TestDiskOverwriteFromXML(t *testing.T) {
	xml := `<Storage><overWrite>true</overWrite></Storage>`
	f := &fakeFetcher{get: map[string]string{"/ContentMgmt/storage": xml}}
	s := &Service{F: f}
	ow := s.DiskOverwriteStatus(nil)
	if ow.Enabled == nil || !*ow.Enabled || ow.Label != "已开启" {
		t.Fatalf("ow = %+v", ow)
	}
	if !strings.Contains(ow.Detail, "overWrite") {
		t.Fatalf("detail = %s", ow.Detail)
	}
}

func TestDiskOverwriteInfer(t *testing.T) {
	f := &fakeFetcher{get: map[string]string{}}
	s := &Service{F: f}
	drives := []model.Drive{{Usage: "99.0%", Status: "ok"}}
	ow := s.DiskOverwriteStatus(drives)
	if ow.Label != "推断已开启" || ow.Enabled == nil || !*ow.Enabled {
		t.Fatalf("ow = %+v", ow)
	}
	drives = []model.Drive{{Usage: "99.0%", Status: "error"}}
	ow = s.DiskOverwriteStatus(drives)
	if ow.Label != "推断未开启" || ow.Enabled == nil || *ow.Enabled {
		t.Fatalf("ow = %+v", ow)
	}
	drives = []model.Drive{{Usage: "50%", Status: "ok"}}
	ow = s.DiskOverwriteStatus(drives)
	if ow.Label != "未知" || ow.Enabled != nil {
		t.Fatalf("ow = %+v", ow)
	}
}

func TestScanOverwriteDeeplyNested(t *testing.T) {
	const depth = 20000
	var b strings.Builder
	b.WriteString(strings.Repeat("<a>", depth))
	b.WriteString("<overWrite>true</overWrite>")
	b.WriteString(strings.Repeat("</a>", depth))
	f := &fakeFetcher{get: map[string]string{"/ContentMgmt/storage": b.String()}}
	s := &Service{F: f}
	ow := s.DiskOverwriteStatus(nil)
	if ow.Enabled == nil || !*ow.Enabled {
		t.Fatalf("ow = %+v, want enabled=true", ow)
	}
}

func TestParseStrategyNegatedOverwrite(t *testing.T) {
	for _, v := range []string{"no_overwrite", "not_overwrite", "no-overwrite", "not-overwrite", "non_overwrite", "non-overwrite"} {
		got, ok := parseStrategy(v)
		if !ok || got {
			t.Errorf("parseStrategy(%q) = (%v, %v), want (false, true)", v, got, ok)
		}
	}
}

type countingFetcher struct {
	get   map[string]string
	calls map[string]int
}

func (f *countingFetcher) Get(endpoint string) (string, error) {
	if f.calls == nil {
		f.calls = map[string]int{}
	}
	f.calls[endpoint]++
	return f.get[endpoint], nil
}
func (f *countingFetcher) Context() context.Context { return context.Background() }

func TestStorageCachesEndpointWithinService(t *testing.T) {
	f := &countingFetcher{get: map[string]string{
		"/ContentMgmt/storage": `<Storage><hdd><hddName>hdd1</hddName><capacity>1048576</capacity><freeSpace>0</freeSpace><status>ok</status></hdd><overWrite>true</overWrite></Storage>`,
	}}
	s := &Service{F: f}
	drives, err := s.StorageStatus()
	if err != nil || len(drives) != 1 {
		t.Fatalf("StorageStatus = (%d, %v)", len(drives), err)
	}
	ow := s.DiskOverwriteStatus(drives)
	if ow.Enabled == nil || !*ow.Enabled {
		t.Fatalf("ow = %+v", ow)
	}
	if got := f.calls["/ContentMgmt/storage"]; got != 1 {
		t.Fatalf("/ContentMgmt/storage fetched %d times, want 1", got)
	}
}

func TestStorageStatusNonNumericFreeSpaceIsUnknown(t *testing.T) {
	xml := `<Storage><hdd><hddName>hdd1</hddName><capacity>10485760</capacity><freeSpace>unknown</freeSpace><status>ok</status></hdd></Storage>`
	f := &fakeFetcher{get: map[string]string{"/ContentMgmt/storage": xml}}
	s := &Service{F: f}
	drives, err := s.StorageStatus()
	if err != nil || len(drives) != 1 {
		t.Fatalf("StorageStatus = (%d, %v)", len(drives), err)
	}
	if drives[0].Usage != "未知" || drives[0].FreeTB != "未知" || drives[0].UsedTB != "未知" {
		t.Fatalf("non-numeric freeSpace should stay 未知, got %+v", drives[0])
	}
}
