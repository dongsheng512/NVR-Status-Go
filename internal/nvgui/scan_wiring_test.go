package nvgui

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"
	"time"

	"nvrstatus/internal/model"
)

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
	handle("/ISAPI/System/deviceInfo", `<DeviceInfo><deviceName>NVR-GUI</deviceName><deviceID>1</deviceID><model>DS-GUI</model><serialNumber>SN</serialNumber><firmwareVersion>1.0</firmwareVersion><macAddress>00:11:22:33:44:55</macAddress><deviceType>NVR</deviceType></DeviceInfo>`)
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

func TestGUIScanWiring(t *testing.T) {
	srv := fakeNVR(t)
	defer srv.Close()
	u, _ := url.Parse(srv.URL)
	port, _ := strconv.Atoi(u.Port())

	app := newTestApp(t)
	app.devices = []model.Device{{Name: "NVR-GUI", IP: u.Hostname(), Port: port, Username: "admin", Password: "pw"}}
	app.target = "NVR-GUI"
	app.opt = model.DefaultScanOptions()
	app.syncFormFromOptions()

	app.startScan()
	done := app.ScanDone()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("scan did not finish")
	}
	if app.report == nil {
		t.Fatalf("scan did not finish; scanning=%v logs=%v", app.scanning, app.logs)
	}
	if app.report.Error != "" {
		t.Fatalf("report error: %s", app.report.Error)
	}
	if len(app.report.Records) != 1 || app.report.Records[0].Name != "前门" {
		t.Fatalf("records = %+v", app.report.Records)
	}
	if app.progress != 1 {
		t.Fatalf("progress = %v", app.progress)
	}
}
