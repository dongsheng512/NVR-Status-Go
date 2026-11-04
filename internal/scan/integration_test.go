package scan

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"nvrstatus/internal/model"
)

// fakeNVR serves the minimal ISAPI surface a quick scan needs. It issues a
// Digest challenge once and accepts any Authorization afterwards.
func fakeNVR(t *testing.T) *httptest.Server {
	t.Helper()
	now := time.Now().UTC()
	start := now.Add(-10 * time.Minute).Format("2006-01-02T15:04:05Z")
	end := now.Add(-5 * time.Minute).Format("2006-01-02T15:04:05Z")
	mux := http.NewServeMux()
	handle := func(path, body string) {
		mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") == "" {
				w.Header().Set("WWW-Authenticate", `Digest realm="test", nonce="abc123", qop="auth"`)
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			w.Header().Set("Content-Type", "application/xml")
			fmt.Fprint(w, body)
		})
	}
	handle("/ISAPI/System/deviceInfo", `<DeviceInfo><deviceName>NVR-T</deviceName><deviceID>1</deviceID><model>DS-TEST</model><serialNumber>SN1</serialNumber><firmwareVersion>1.0</firmwareVersion><macAddress>00:11:22:33:44:55</macAddress><deviceType>NVR</deviceType></DeviceInfo>`)
	handle("/ISAPI/System/status", `<SystemStatus><currentDeviceTime>2026-09-11T12:00:00+08:00</currentDeviceTime><deviceUpTime>90061</deviceUpTime><memoryUsage>100</memoryUsage><memoryAvailable>300</memoryAvailable></SystemStatus>`)
	handle("/ISAPI/ContentMgmt/InputProxy/channels", `<InputProxyChannelList><InputProxyChannel><id>1</id><name>前门</name><ipAddress>192.0.2.11</ipAddress><model>IPC</model></InputProxyChannel></InputProxyChannelList>`)
	handle("/ISAPI/ContentMgmt/InputProxy/channels/status", `<InputProxyChannelStatusList><InputProxyChannelStatus><id>1</id><online>true</online><chanDetectResult>connect</chanDetectResult></InputProxyChannelStatus></InputProxyChannelStatusList>`)
	handle("/ISAPI/ContentMgmt/record/tracks", `<TrackList><Track><id>101</id><Channel>101</Channel><Actions><Record>true</Record><ActionRecordingMode>CMR</ActionRecordingMode></Actions><CustomExtension><SaveAudio>true</SaveAudio></CustomExtension></Track></TrackList>`)
	handle("/ISAPI/ContentMgmt/storage", `<Storage><hdd><hddName>hdd1</hddName><capacity>10485760</capacity><freeSpace>5242880</freeSpace><status>ok</status><hddType>SATA</hddType><property>RW</property><manufacturer>WD</manufacturer></hdd></Storage>`)
	mux.HandleFunc("/ISAPI/ContentMgmt/search", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "" {
			w.Header().Set("WWW-Authenticate", `Digest realm="test", nonce="abc123", qop="auth"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/xml")
		fmt.Fprintf(w, `<CMSearchResult><totalMatches>1</totalMatches><searchMatchItem><timeSpan><startTime>%s</startTime><endTime>%s</endTime></timeSpan><mediaSegmentDescriptor><playbackURI>rtsp://192.0.2.11:554/Streaming/tracks/101?starttime=x&amp;endtime=y</playbackURI></mediaSegmentDescriptor></searchMatchItem></CMSearchResult>`, start, end)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		fmt.Fprint(w, `<ResponseStatus><statusCode>4</statusCode><statusString>Invalid Operation</statusString></ResponseStatus>`)
	})
	return httptest.NewServer(mux)
}

func serverDevice(t *testing.T, srv *httptest.Server) model.Device {
	t.Helper()
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	port, _ := strconv.Atoi(u.Port())
	return model.Device{Name: "NVR-T", IP: u.Hostname(), Port: port, Username: "admin", Password: "pw"}
}

func TestRunQuickScanEndToEnd(t *testing.T) {
	srv := fakeNVR(t)
	defer srv.Close()
	r := NewRunner(Options{Quiet: true, Tools: noTools})
	opt := model.DefaultScanOptions()
	opt.Workers = 2
	rep := r.Run(serverDevice(t, srv), opt, "NVR-T", nil)
	if rep.Error != "" {
		t.Fatalf("error: %s", rep.Error)
	}
	if rep.Info["型号"] != "DS-TEST" {
		t.Fatalf("model = %q", rep.Info["型号"])
	}
	if len(rep.Cameras) != 1 || rep.Cameras[0].Name != "前门" || rep.Cameras[0].Online != "true" {
		t.Fatalf("cameras = %+v", rep.Cameras)
	}
	if len(rep.Records) != 1 {
		t.Fatalf("records = %+v", rep.Records)
	}
	rec := rep.Records[0]
	if rec.Channel != "1" || rec.Name != "前门" || rec.DiskStatus != model.StatusOK {
		t.Fatalf("record = %+v", rec)
	}
	if rec.RecordingOK != model.StatusOK || rec.HasAudio == nil || !*rec.HasAudio {
		t.Fatalf("record verdict = %+v", rec)
	}
	if len(rep.Drives) != 1 || rep.Drives[0].CapacityTB != "10.0" {
		t.Fatalf("drives = %+v", rep.Drives)
	}
	if rep.Health.Stats.CameraTotal != 1 || rep.Health.Stats.Online != 1 {
		t.Fatalf("health stats = %+v", rep.Health.Stats)
	}
	if !strings.Contains(rep.Health.Status, "良好") && !strings.Contains(rep.Health.Status, "警告") {
		t.Fatalf("health = %s", rep.Health.Status)
	}
}

func TestRunNoSearchSkipsDisk(t *testing.T) {
	srv := fakeNVR(t)
	defer srv.Close()
	r := NewRunner(Options{Quiet: true, Tools: noTools})
	opt := model.DefaultScanOptions()
	opt.NoSearch = true
	rep := r.Run(serverDevice(t, srv), opt, "NVR-T", nil)
	if rep.Error != "" {
		t.Fatalf("error: %s", rep.Error)
	}
	if len(rep.Records) != 1 || rep.Records[0].DiskStatus != model.StatusSkip {
		t.Fatalf("record = %+v", rep.Records)
	}
	if rep.Records[0].RecordingOK != model.StatusSkip {
		t.Fatalf("recording ok = %s", rep.Records[0].RecordingOK)
	}
}

func TestQueueConcurrentPreservesOrder(t *testing.T) {
	srv1 := fakeNVR(t)
	defer srv1.Close()
	srv2 := fakeNVR(t)
	defer srv2.Close()
	d1 := serverDevice(t, srv1)
	d1.Name = "NVR-A"
	d2 := serverDevice(t, srv2)
	d2.Name = "NVR-B"

	r := NewRunner(Options{Quiet: true, Tools: noTools})
	opt := model.DefaultScanOptions()
	opt.NoSearch = true
	opt.DeviceWorkers = 2
	reports := r.Queue([]model.Device{d1, d2}, opt, nil, nil)
	if len(reports) != 2 {
		t.Fatalf("reports = %d", len(reports))
	}
	if reports[0].DeviceName != "NVR-A" || reports[1].DeviceName != "NVR-B" {
		t.Fatalf("order = %q, %q", reports[0].DeviceName, reports[1].DeviceName)
	}
	if reports[0].Error != "" || reports[1].Error != "" {
		t.Fatalf("errors: %q / %q", reports[0].Error, reports[1].Error)
	}
}
