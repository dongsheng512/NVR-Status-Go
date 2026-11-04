package isapi

import (
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync"
	"testing"
	"time"

	"nvrstatus/internal/model"
)

type routeRecorder struct {
	mu     sync.Mutex
	routes map[string]string
	status map[string]int
	counts map[string]int
}

func newRouteRecorder() *routeRecorder {
	return &routeRecorder{
		routes: make(map[string]string),
		status: make(map[string]int),
		counts: make(map[string]int),
	}
}

func (r *routeRecorder) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	r.mu.Lock()
	body, ok := r.routes[req.URL.Path]
	status := r.status[req.URL.Path]
	if status == 0 {
		status = http.StatusOK
	}
	r.counts[req.URL.Path]++
	r.mu.Unlock()
	if !ok {
		http.NotFound(w, req)
		return
	}
	w.WriteHeader(status)
	_, _ = io.WriteString(w, body)
}

func (r *routeRecorder) count(path string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.counts[path]
}

func TestDeviceInfoMapping(t *testing.T) {
	recorder := newRouteRecorder()
	recorder.routes["/ISAPI/System/deviceInfo"] = `<?xml version="1.0" encoding="UTF-8"?>
<DeviceInfo xmlns="http://www.hikvision.com/ver20/XMLSchema">
  <deviceName>NVR-Office</deviceName>
  <deviceID>device-42</deviceID>
  <model>DS-7616NI-K2</model>
  <serialNumber>SN123456</serialNumber>
  <firmwareVersion>V4.30.085</firmwareVersion>
  <macAddress>AA:BB:CC:DD:EE:FF</macAddress>
  <deviceType>NVR</deviceType>
</DeviceInfo>`
	server := httptest.NewServer(recorder)
	defer server.Close()
	client := newTestClient(t, server, nil)

	info, err := client.DeviceInfo()
	if err != nil {
		t.Fatalf("DeviceInfo: %v", err)
	}
	want := map[string]string{
		"设备名称":  "NVR-Office",
		"设备ID":  "device-42",
		"型号":    "DS-7616NI-K2",
		"序列号":   "SN123456",
		"固件版本":  "V4.30.085",
		"MAC地址": "AA:BB:CC:DD:EE:FF",
		"设备类型":  "NVR",
	}
	if !reflect.DeepEqual(info, want) {
		t.Fatalf("DeviceInfo = %#v\nwant %#v", info, want)
	}
}

func TestDeviceInfoDefaults(t *testing.T) {
	recorder := newRouteRecorder()
	recorder.routes["/ISAPI/System/deviceInfo"] = `<DeviceInfo><deviceName>OnlyName</deviceName></DeviceInfo>`
	server := httptest.NewServer(recorder)
	defer server.Close()
	client := newTestClient(t, server, nil)

	info, err := client.DeviceInfo()
	if err != nil {
		t.Fatalf("DeviceInfo: %v", err)
	}
	if info["设备名称"] != "OnlyName" {
		t.Fatalf("device name = %q", info["设备名称"])
	}
	for _, key := range []string{"设备ID", "型号", "序列号", "固件版本", "MAC地址", "设备类型"} {
		if info[key] != "未知" {
			t.Fatalf("%s = %q, want 未知", key, info[key])
		}
	}
}

func TestSystemStatusMapping(t *testing.T) {
	recorder := newRouteRecorder()
	recorder.routes["/ISAPI/System/status"] = `<DeviceStatus>
  <currentDeviceTime>2026-07-29T12:36:13+08:00</currentDeviceTime>
  <deviceUpTime>90061</deviceUpTime>
  <memoryUsage>1000</memoryUsage>
  <memoryAvailable>3000</memoryAvailable>
</DeviceStatus>`
	server := httptest.NewServer(recorder)
	defer server.Close()
	client := newTestClient(t, server, nil)

	status, err := client.SystemStatus()
	if err != nil {
		t.Fatalf("SystemStatus: %v", err)
	}
	want := map[string]string{
		"当前时间":  "2026-07-29T12:36:13+08:00",
		"运行时长":  "1天1小时1分钟",
		"内存使用":  "1000.0 MB",
		"内存可用":  "3000.0 MB",
		"内存使用率": "25.0%",
	}
	if !reflect.DeepEqual(status, want) {
		t.Fatalf("SystemStatus = %#v\nwant %#v", status, want)
	}
}

func TestSystemStatusMissingFields(t *testing.T) {
	recorder := newRouteRecorder()
	recorder.routes["/ISAPI/System/status"] = `<DeviceStatus/>`
	server := httptest.NewServer(recorder)
	defer server.Close()
	client := newTestClient(t, server, nil)

	status, err := client.SystemStatus()
	if err != nil {
		t.Fatalf("SystemStatus: %v", err)
	}
	if status["当前时间"] != "未知" || status["运行时长"] != "0天0小时0分钟" ||
		status["内存使用"] != "0.0 MB" || status["内存使用率"] != "0.0%" {
		t.Fatalf("status = %#v", status)
	}
}

func TestAlarms(t *testing.T) {
	recorder := newRouteRecorder()
	recorder.routes["/ISAPI/ContentMgmt/storage"] = `<Storage>
  <hddList>
    <hdd><hddName>/dev/sda</hddName><status>ok</status></hdd>
    <hdd><hddName>/dev/sdb</hddName><status>notexist</status></hdd>
  </hddList>
</Storage>`
	server := httptest.NewServer(recorder)
	defer server.Close()
	client := newTestClient(t, server, nil)

	alarms, err := client.Alarms()
	if err != nil {
		t.Fatalf("Alarms: %v", err)
	}
	if len(alarms) != 1 {
		t.Fatalf("alarms = %#v", alarms)
	}
	alarm := alarms[0]
	if alarm.Type != "硬盘异常" || alarm.Disk != "/dev/sdb" || alarm.Level != "notexist" {
		t.Fatalf("alarm = %#v", alarm)
	}
	if alarm.Description != "硬盘状态异常,可能已满或故障" {
		t.Fatalf("description = %q", alarm.Description)
	}
}

func TestCamerasMergeStatusAndSort(t *testing.T) {
	recorder := newRouteRecorder()
	recorder.routes["/ISAPI/ContentMgmt/InputProxy/channels"] = `<InputProxyChannelList>
  <InputProxyChannel>
    <id>10</id><name>十号</name>
    <channel><ipAddress>10.0.0.10</ipAddress><model>DS-2CD10</model></channel>
  </InputProxyChannel>
  <InputProxyChannel>
    <id>2</id><name>二号</name>
    <channel><ipAddress>10.0.0.2</ipAddress><model>DS-2CD2</model></channel>
  </InputProxyChannel>
  <InputProxyChannel>
    <id>1</id><name>一号</name>
    <channel><ipAddress>10.0.0.1</ipAddress><model>DS-2CD1</model></channel>
  </InputProxyChannel>
</InputProxyChannelList>`
	recorder.routes["/ISAPI/ContentMgmt/InputProxy/channels/status"] = `<InputProxyChannelStatusList>
  <InputProxyChannelStatus>
    <id>1</id><online>true</online><chanDetectResult>ok</chanDetectResult>
  </InputProxyChannelStatus>
  <InputProxyChannelStatus>
    <id>10</id><online>false</online><chanDetectResult>offline</chanDetectResult>
  </InputProxyChannelStatus>
</InputProxyChannelStatusList>`
	server := httptest.NewServer(recorder)
	defer server.Close()
	client := newTestClient(t, server, nil)

	want := []model.Camera{
		{ID: "1", Name: "一号", IP: "10.0.0.1", Model: "DS-2CD1", Online: "true", DetectState: "ok"},
		{ID: "2", Name: "二号", IP: "10.0.0.2", Model: "DS-2CD2", Online: "unknown", DetectState: "unknown"},
		{ID: "10", Name: "十号", IP: "10.0.0.10", Model: "DS-2CD10", Online: "false", DetectState: "offline"},
	}
	for i := 0; i < 2; i++ {
		cameras, err := client.Cameras()
		if err != nil {
			t.Fatalf("Cameras #%d: %v", i, err)
		}
		if !reflect.DeepEqual(cameras, want) {
			t.Fatalf("Cameras #%d = %#v\nwant %#v", i, cameras, want)
		}
	}
	if got := recorder.count("/ISAPI/ContentMgmt/InputProxy/channels"); got != 1 {
		t.Fatalf("channel requests = %d, want 1", got)
	}
	if got := recorder.count("/ISAPI/ContentMgmt/InputProxy/channels/status"); got != 1 {
		t.Fatalf("status requests = %d, want 1", got)
	}
}

func TestCamerasFallbackToStreaming(t *testing.T) {
	recorder := newRouteRecorder()
	// Missing InputProxy endpoint: returns 404 -> Python falls back.
	recorder.status["/ISAPI/ContentMgmt/InputProxy/channels"] = http.StatusNotFound
	recorder.routes["/ISAPI/ContentMgmt/InputProxy/channels"] = "not supported"
	recorder.routes["/ISAPI/ContentMgmt/Streaming/channels"] = `<StreamingChannelList>
  <StreamingChannel><id>102</id><enabled>false</enabled></StreamingChannel>
  <StreamingChannel><id>201</id><enabled>false</enabled></StreamingChannel>
  <StreamingChannel><id>101</id><enabled>true</enabled></StreamingChannel>
</StreamingChannelList>`
	server := httptest.NewServer(recorder)
	defer server.Close()
	client := newTestClient(t, server, nil)

	want := []model.Camera{
		{ID: "1", Name: "未知", IP: "未知", Model: "未知", Online: "true", DetectState: "已配置"},
		{ID: "2", Name: "未知", IP: "未知", Model: "未知", Online: "false", DetectState: "已配置"},
	}
	for i := 0; i < 2; i++ {
		cameras, err := client.Cameras()
		if err != nil {
			t.Fatalf("Cameras #%d: %v", i, err)
		}
		if !reflect.DeepEqual(cameras, want) {
			t.Fatalf("Cameras #%d = %#v\nwant %#v", i, cameras, want)
		}
	}
	if got := recorder.count("/ISAPI/ContentMgmt/Streaming/channels"); got != 1 {
		t.Fatalf("streaming requests = %d, want 1", got)
	}
}

func TestCamerasFallbackOnResponseStatus(t *testing.T) {
	recorder := newRouteRecorder()
	recorder.routes["/ISAPI/ContentMgmt/InputProxy/channels"] = `<ResponseStatus>
  <statusCode>4</statusCode>
  <statusString>Invalid Operation</statusString>
</ResponseStatus>`
	recorder.routes["/ISAPI/ContentMgmt/Streaming/channels"] = `<StreamingChannelList>
  <StreamingChannel><id>101</id><enabled>true</enabled></StreamingChannel>
</StreamingChannelList>`
	server := httptest.NewServer(recorder)
	defer server.Close()
	client := newTestClient(t, server, nil)

	cameras, err := client.Cameras()
	if err != nil {
		t.Fatalf("Cameras: %v", err)
	}
	if len(cameras) != 1 || cameras[0].ID != "1" || cameras[0].DetectState != "已配置" {
		t.Fatalf("cameras = %#v", cameras)
	}
	if got := recorder.count("/ISAPI/ContentMgmt/InputProxy/channels/status"); got != 0 {
		t.Fatalf("status requests = %d, want 0", got)
	}
}

func TestDeviceLocationFromSystemTime(t *testing.T) {
	recorder := newRouteRecorder()
	recorder.routes["/ISAPI/System/time"] = `<Time>
  <timeMode>manual</timeMode>
  <localTime>2026-07-29T12:36:13+08:00</localTime>
  <timeZone>CST-8:00:00</timeZone>
</Time>`
	server := httptest.NewServer(recorder)
	defer server.Close()
	client := newTestClient(t, server, nil)

	if got := zoneOffset(client.DeviceLocation()); got != 8*3600 {
		t.Fatalf("offset = %d, want %d", got, 8*3600)
	}
}

func TestDeviceLocationFallsBackToSystemStatus(t *testing.T) {
	recorder := newRouteRecorder()
	recorder.status["/ISAPI/System/time"] = http.StatusNotFound
	recorder.routes["/ISAPI/System/time"] = "not supported"
	recorder.routes["/ISAPI/System/status"] = `<DeviceStatus>
  <currentDeviceTime>2026-07-29T12:36:13-05:00</currentDeviceTime>
  <deviceUpTime>10</deviceUpTime>
</DeviceStatus>`
	server := httptest.NewServer(recorder)
	defer server.Close()
	client := newTestClient(t, server, nil)

	if got := zoneOffset(client.DeviceLocation()); got != -5*3600 {
		t.Fatalf("offset = %d, want %d", got, -5*3600)
	}
}

func TestDeviceLocationFallsBackToLocal(t *testing.T) {
	recorder := newRouteRecorder()
	recorder.status["/ISAPI/System/time"] = http.StatusNotFound
	recorder.routes["/ISAPI/System/time"] = "not supported"
	recorder.routes["/ISAPI/System/status"] = `<DeviceStatus><currentDeviceTime>unknown</currentDeviceTime></DeviceStatus>`
	server := httptest.NewServer(recorder)
	defer server.Close()
	client := newTestClient(t, server, nil)

	if got := client.DeviceLocation(); got != time.Local {
		t.Fatalf("location = %v, want time.Local", got)
	}
}

func TestLocationFromTimezonePOSIXSign(t *testing.T) {
	if got := zoneOffset(locationFromTimezone("CST-8:00:00")); got != 8*3600 {
		t.Fatalf("CST-8 offset = %d", got)
	}
	if got := zoneOffset(locationFromTimezone("+05:30")); got != 5*3600+30*60 {
		t.Fatalf("+05:30 offset = %d", got)
	}
	if got := zoneOffset(locationFromTimezone("UTC+05:30")); got != 5*3600+30*60 {
		t.Fatalf("UTC+05:30 offset = %d", got)
	}
}

func zoneOffset(location *time.Location) int {
	if location == nil {
		return 0
	}
	_, offset := time.Date(2026, 1, 1, 0, 0, 0, 0, location).Zone()
	return offset
}
