package recording

import (
	"strings"
	"testing"
	"time"

	"nvrstatus/internal/model"
	"nvrstatus/internal/xmlnode"
)

type fakeFetcher struct {
	get     map[string]string
	post    map[string]string
	cameras []model.Camera
	loc     *time.Location
}

func (f *fakeFetcher) Get(endpoint string) (string, error) { return f.get[endpoint], nil }
func (f *fakeFetcher) Post(endpoint, body string) (string, error) {
	return f.post[endpoint], nil
}
func (f *fakeFetcher) DeviceLocation() *time.Location   { return f.loc }
func (f *fakeFetcher) Cameras() ([]model.Camera, error) { return f.cameras, nil }
func (f *fakeFetcher) CheckCancel() error               { return nil }

func TestPhysicalChannel(t *testing.T) {
	cases := []struct{ xml, want string }{
		{`<Track><id>101</id><Channel>101</Channel></Track>`, "1"},
		{`<Track><id>102</id><Channel>102</Channel></Track>`, "1"},
		{`<Track><id>6401</id><Channel>6401</Channel></Track>`, "64"},
		{`<Track><id>6402</id><Channel>6402</Channel></Track>`, "64"},
		{`<Track><id>100</id><Channel>100</Channel></Track>`, "100"},
		{`<Track><id>abc</id><Channel>abc</Channel></Track>`, "abc"},
		{`<Track><id>9</id><Channel>9</Channel><SrcChannel>3</SrcChannel></Track>`, "3"},
	}
	for _, c := range cases {
		root, err := xmlnode.Parse(c.xml)
		if err != nil {
			t.Fatal(err)
		}
		if got := PhysicalChannel(root); got != c.want {
			t.Errorf("%s: got %s want %s", c.xml, got, c.want)
		}
	}
}

func TestSaveAudio(t *testing.T) {
	root, _ := xmlnode.Parse(`<Track><CustomExtension><SaveAudio>true</SaveAudio></CustomExtension></Track>`)
	if got := SaveAudio(root); got == nil || !*got {
		t.Fatalf("want true, got %v", got)
	}
	root2, _ := xmlnode.Parse(`<Track><CustomExtension><SaveAudio>false</SaveAudio></CustomExtension></Track>`)
	if got := SaveAudio(root2); got == nil || *got {
		t.Fatalf("want false, got %v", got)
	}
	root3, _ := xmlnode.Parse(`<Track/>`)
	if got := SaveAudio(root3); got != nil {
		t.Fatalf("want nil, got %v", *got)
	}
}

const tracksXML = `<TrackList>
 <Track>
  <id>102</id><Channel>102</Channel>
  <Actions><Record>true</Record><ActionRecordingMode>MDR</ActionRecordingMode></Actions>
 </Track>
 <Track>
  <id>101</id><Channel>101</Channel>
  <Actions><Record>true</Record><ActionRecordingMode>CMR</ActionRecordingMode></Actions>
  <CustomExtension><SaveAudio>true</SaveAudio></CustomExtension>
 </Track>
 <Track>
  <id>201</id><Channel>201</Channel>
  <Actions><Record>false</Record></Actions>
 </Track>
</TrackList>`

func TestRecordingStatusMergesStreams(t *testing.T) {
	f := &fakeFetcher{
		get: map[string]string{"/ContentMgmt/record/tracks": tracksXML},
		cameras: []model.Camera{
			{ID: "1", Name: "前门", Online: "true"},
			{ID: "2", Name: "后门", Online: "false"},
		},
		loc: time.UTC,
	}
	s := &Service{F: f, LookbackMinutes: 60, SearchWorkers: 2, CheckDiskRecording: false,
		BusyStartHour: 10, BusyEndHour: 18, Now: func() time.Time { return time.Date(2026, 9, 11, 12, 0, 0, 0, time.Local) }}
	records, err := s.RecordingStatus()
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 2 {
		t.Fatalf("records = %d, want 2 (main/sub merged)", len(records))
	}
	if records[0].Channel != "1" || records[0].TrackID != "101" {
		t.Fatalf("first = %+v", records[0])
	}
	if records[0].Name != "前门" || records[0].RecordingMode != "连续录像" || records[0].RecordingSchedule != "1/1" {
		t.Fatalf("first fields = %+v", records[0])
	}
	if records[0].HasAudio == nil || !*records[0].HasAudio {
		t.Fatalf("hasAudio = %v", records[0].HasAudio)
	}
	if records[0].DiskStatus != model.StatusSkip || records[0].DiskDetail != "已跳过近期录像检查" {
		t.Fatalf("disk = %s/%s", records[0].DiskStatus, records[0].DiskDetail)
	}
	if records[1].Channel != "2" || records[1].RecordingOK != "未配置" {
		t.Fatalf("second = %+v", records[1])
	}
}

const searchXML = `<CMSearchResult>
 <totalMatches>1</totalMatches>
 <searchMatchItem>
  <timeSpan><startTime>2026-09-11T03:00:00Z</startTime><endTime>2026-09-11T03:30:00Z</endTime></timeSpan>
  <mediaSegmentDescriptor><playbackURI>rtsp://192.0.2.10:554/Streaming/tracks/101?starttime=20260911T030000Z&amp;endtime=20260911T033000Z</playbackURI></mediaSegmentDescriptor>
 </searchMatchItem>
</CMSearchResult>`

func TestSearchTrackRecent(t *testing.T) {
	now := time.Date(2026, 9, 11, 3, 35, 0, 0, time.UTC)
	f := &fakeFetcher{
		post: map[string]string{"/ContentMgmt/search": searchXML},
		loc:  time.UTC,
	}
	s := &Service{F: f, LookbackMinutes: 60, SearchWorkers: 1, Now: func() time.Time { return now }}
	res := s.SearchTrackRecent("101")
	if !res.OK || res.Status != model.StatusOK {
		t.Fatalf("res = %+v", res)
	}
	if res.Segments != 1 || res.PlaybackURI == "" || !strings.Contains(res.Detail, "近期有录像") {
		t.Fatalf("res = %+v", res)
	}
	if res.LatestEnd == nil || !res.LatestEnd.Equal(time.Date(2026, 9, 11, 3, 30, 0, 0, time.UTC)) {
		t.Fatalf("latest = %v", res.LatestEnd)
	}
}

func TestSearchTrackRecentNoMatches(t *testing.T) {
	f := &fakeFetcher{post: map[string]string{"/ContentMgmt/search": `<CMSearchResult><totalMatches>0</totalMatches></CMSearchResult>`}, loc: time.UTC}
	s := &Service{F: f, LookbackMinutes: 60, Now: func() time.Time { return time.Date(2026, 9, 11, 3, 35, 0, 0, time.UTC) }}
	res := s.SearchTrackRecent("101")
	if res.Status != model.StatusBad || !strings.Contains(res.Detail, "无录像片段") {
		t.Fatalf("res = %+v", res)
	}
}

func TestBusyWindowAndPick(t *testing.T) {
	now := time.Date(2026, 9, 11, 12, 0, 0, 0, time.Local)
	s := &Service{BusyStartHour: 10, BusyEndHour: 18, Now: func() time.Time { return now }}
	ws, we, label := s.BusyHoursWindow()
	if ws.Local().Format("15:04") != "10:00" {
		t.Fatalf("ws = %v label=%s", ws, label)
	}
	if we.Local().Format("15:04") != "11:50" {
		t.Fatalf("we = %v", we)
	}
	clipS, clipE, _ := s.PickBusyClipTimes(6, nil)
	if clipS.Before(ws) || clipE.After(we) || clipE.Sub(clipS) != 6*time.Second {
		t.Fatalf("clip %v..%v outside %v..%v", clipS, clipE, ws, we)
	}
}

func TestPickBusyClipTimesFixedInstant(t *testing.T) {
	now := time.Date(2026, 9, 11, 12, 0, 0, 0, time.Local)
	at := time.Date(2026, 9, 11, 9, 5, 0, 0, time.Local)
	s := &Service{BusyStartHour: 10, BusyEndHour: 18, Now: func() time.Time { return now }}
	clipS, clipE, label := s.PickBusyClipTimes(6, &at)
	if !clipS.Equal(at) && !clipS.Equal(at.Add(-6*time.Second)) {
		t.Fatalf("clipS = %v", clipS)
	}
	if clipE.Sub(clipS) != 6*time.Second {
		t.Fatalf("duration %v", clipE.Sub(clipS))
	}
	if !strings.Contains(label, "定点抽检 09-11 09:05:00") || !strings.Contains(label, "抽检点") {
		t.Fatalf("label = %s", label)
	}
}

func TestJudgeRecordingOK(t *testing.T) {
	yes := true
	no := false
	cases := []struct {
		rec  model.Record
		want string
	}{
		{model.Record{RecordingEnabled: &no}, "未配置"},
		{model.Record{RecordingEnabled: &yes, DiskStatus: model.StatusSkip}, "跳过"},
		{model.Record{RecordingEnabled: &yes, DiskStatus: model.StatusBad}, "异常"},
		{model.Record{RecordingEnabled: &yes, DiskStatus: model.StatusUnknown}, "未知"},
		{model.Record{RecordingEnabled: &yes, DiskStatus: model.StatusOK}, "正常"},
		{model.Record{RecordingEnabled: &yes, DiskStatus: model.StatusOK, VideoProbe: model.StatusBad}, "异常"},
	}
	for _, c := range cases {
		if got := judgeRecordingOK(&c.rec); got != c.want {
			t.Errorf("rec=%+v got %s want %s", c.rec, got, c.want)
		}
	}
}
