package isapi

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"nvrstatus/internal/model"
	"nvrstatus/internal/util"
)

// defaultUnknown is used for missing text fields, matching the Python client's
// findtext defaults.
const defaultUnknown = "未知"

// maxSortID sends unparseable channel ids to the end of the list.
const maxSortID = 1 << 30

// DeviceInfo returns the /System/deviceInfo fields using the same keys as the
// Python get_device_info implementation.
func (c *Client) DeviceInfo() (map[string]string, error) {
	root, err := c.Parse("/System/deviceInfo", "设备信息")
	if err != nil {
		return map[string]string{}, err
	}
	if root == nil {
		return map[string]string{}, nil
	}
	return map[string]string{
		"设备名称":  root.FindText("deviceName", defaultUnknown),
		"设备ID":  root.FindText("deviceID", defaultUnknown),
		"型号":    root.FindText("model", defaultUnknown),
		"序列号":   root.FindText("serialNumber", defaultUnknown),
		"固件版本":  root.FindText("firmwareVersion", defaultUnknown),
		"MAC地址": root.FindText("macAddress", defaultUnknown),
		"设备类型":  root.FindText("deviceType", defaultUnknown),
	}, nil
}

// SystemStatus returns the /System/status fields using the same keys and
// formatting as the Python get_system_status implementation.
func (c *Client) SystemStatus() (map[string]string, error) {
	root, err := c.Parse("/System/status", "系统状态")
	if err != nil {
		return map[string]string{}, err
	}
	if root == nil {
		return map[string]string{}, nil
	}

	memoryUsage := util.ToFloat(root.FindText(".//memoryUsage", ""), 0)
	memoryAvailable := util.ToFloat(root.FindText(".//memoryAvailable", ""), 0)
	totalMemory := memoryUsage + memoryAvailable
	memoryUsageRate := 0.0
	if totalMemory > 0 {
		memoryUsageRate = memoryUsage / totalMemory * 100
	}

	upTime := util.ToInt(root.FindText("deviceUpTime", ""), 0)
	upDays := upTime / 86400
	upHours := (upTime % 86400) / 3600
	upMinutes := (upTime % 3600) / 60

	return map[string]string{
		"当前时间":  root.FindText("currentDeviceTime", defaultUnknown),
		"运行时长":  fmt.Sprintf("%d天%d小时%d分钟", upDays, upHours, upMinutes),
		"内存使用":  fmt.Sprintf("%.1f MB", memoryUsage),
		"内存可用":  fmt.Sprintf("%.1f MB", memoryAvailable),
		"内存使用率": fmt.Sprintf("%.1f%%", memoryUsageRate),
	}, nil
}

// Alarms returns one alarm for every disk whose status is not "ok". The source
// endpoint is /ContentMgmt/storage, shared (and cached) with storage queries.
func (c *Client) Alarms() ([]model.Alarm, error) {
	root, err := c.Parse("/ContentMgmt/storage", "硬盘状态")
	if err != nil {
		return nil, err
	}
	if root == nil {
		return []model.Alarm{}, nil
	}
	alarms := make([]model.Alarm, 0)
	for _, hdd := range root.FindAll(".//hdd") {
		status := hdd.FindText("status", "unknown")
		if status == "ok" {
			continue
		}
		alarms = append(alarms, model.Alarm{
			Type:        "硬盘异常",
			Disk:        hdd.FindText("hddName", defaultUnknown),
			Description: "硬盘状态异常,可能已满或故障",
			Level:       status,
		})
	}
	return alarms, nil
}

// Cameras returns the input-proxy channel list merged with per-channel online
// status. The result is cached for the lifetime of this client (one scan).
// Devices without InputProxy fall back to Streaming/channels, where only the
// configured main-stream channels can be discovered.
func (c *Client) Cameras() ([]model.Camera, error) {
	if err := c.CheckCancel(); err != nil {
		return nil, err
	}
	c.camerasMu.Lock()
	defer c.camerasMu.Unlock()
	if c.camerasLoaded {
		return copyCameras(c.cameras), nil
	}

	cameras, supported, err := c.inputProxyCameras()
	if err != nil {
		return nil, err
	}
	if !supported {
		cameras, err = c.streamingChannels()
		if err != nil {
			return nil, err
		}
	}
	c.cameras = copyCameras(cameras)
	c.camerasLoaded = true
	return copyCameras(cameras), nil
}

func copyCameras(cameras []model.Camera) []model.Camera {
	if cameras == nil {
		return []model.Camera{}
	}
	out := make([]model.Camera, len(cameras))
	copy(out, cameras)
	return out
}

// inputProxyCameras returns (cameras, supported, error). supported is false
// when the endpoint is missing or answers with ResponseStatus, in which case
// the caller must use the streaming fallback.
func (c *Client) inputProxyCameras() ([]model.Camera, bool, error) {
	infoRoot, err := c.Parse("/ContentMgmt/InputProxy/channels", "摄像头列表")
	if err != nil {
		if errors.Is(err, util.ErrCancelled) {
			return nil, false, err
		}
		// Any other failure means "unsupported" for parity with Python, which
		// treats a nil root as a signal to try Streaming/channels.
		infoRoot = nil
	}
	if infoRoot == nil || infoRoot.Tag == "ResponseStatus" {
		return nil, false, nil
	}

	statusRoot, err := c.Parse("/ContentMgmt/InputProxy/channels/status", "摄像头状态")
	if err != nil {
		if errors.Is(err, util.ErrCancelled) {
			return nil, false, err
		}
		// Keep the camera list even if status is unavailable; online stays
		// "unknown", matching Python's empty online_map behavior.
		statusRoot = nil
	}

	type channelStatus struct {
		online      string
		detectState string
	}
	onlineMap := make(map[string]channelStatus)
	if statusRoot != nil && statusRoot.Tag != "ResponseStatus" {
		for _, status := range statusRoot.FindAll(".//InputProxyChannelStatus") {
			id := status.FindText("id", "")
			onlineMap[id] = channelStatus{
				online:      status.FindText("online", "unknown"),
				detectState: status.FindText("chanDetectResult", "unknown"),
			}
		}
	}

	cameras := make([]model.Camera, 0)
	for _, channel := range infoRoot.FindAll(".//InputProxyChannel") {
		id := channel.FindText("id", defaultUnknown)
		status := onlineMap[id]
		online := status.online
		if online == "" {
			online = "unknown"
		}
		detectState := status.detectState
		if detectState == "" {
			detectState = "unknown"
		}
		cameras = append(cameras, model.Camera{
			ID:          id,
			Name:        channel.FindText("name", defaultUnknown),
			IP:          channel.FindText(".//ipAddress", defaultUnknown),
			Model:       channel.FindText(".//model", defaultUnknown),
			Online:      online,
			DetectState: detectState,
		})
	}
	sortCameras(cameras)
	return cameras, true, nil
}

// streamingChannels is the fallback used when InputProxy is unavailable. Only
// main-stream ids ending in "01" represent a physical camera; that suffix is
// stripped from the reported channel id.
func (c *Client) streamingChannels() ([]model.Camera, error) {
	root, err := c.Parse("/ContentMgmt/Streaming/channels", "摄像头通道")
	if err != nil {
		return nil, err
	}
	if root == nil {
		return []model.Camera{}, nil
	}
	cameras := make([]model.Camera, 0)
	for _, channel := range root.FindAll(".//StreamingChannel") {
		id := channel.FindText("id", defaultUnknown)
		if !strings.HasSuffix(id, "01") {
			continue
		}
		physicalID := id
		if len(id) > 2 {
			physicalID = id[:len(id)-2]
		}
		cameras = append(cameras, model.Camera{
			ID:          physicalID,
			Name:        defaultUnknown,
			IP:          defaultUnknown,
			Model:       defaultUnknown,
			Online:      channel.FindText("enabled", "unknown"),
			DetectState: "已配置",
		})
	}
	sortCameras(cameras)
	return cameras, nil
}

func sortCameras(cameras []model.Camera) {
	sort.SliceStable(cameras, func(i, j int) bool {
		return util.ToInt(cameras[i].ID, maxSortID) < util.ToInt(cameras[j].ID, maxSortID)
	})
}

var (
	offsetSuffix = regexp.MustCompile(`([+-])(\d{2}):?(\d{2})$`)
	zoneName     = regexp.MustCompile(`^([A-Za-z]+)?([+-])(\d{1,2}):(\d{2})(?::(\d{2}))?$`)
)

// DeviceLocation resolves the device timezone offset. It first probes
// /System/time (localTime or Hikvision/POSIX timeZone), then falls back to the
// currentDeviceTime offset exposed by /System/status, and finally to the local
// machine timezone. Only successfully parsed offsets are cached, so a transient
// failure can be retried later.
func (c *Client) DeviceLocation() *time.Location {
	c.tzMu.Lock()
	defer c.tzMu.Unlock()
	if c.tz != nil {
		return c.tz
	}
	if err := c.CheckCancel(); err != nil {
		return time.Local
	}

	if root, err := c.parse("/System/time", "设备时间", true); err == nil && root != nil {
		if location := locationFromRawTime(root.FindText("localTime", "")); location != nil {
			c.tz = location
			return location
		}
		if location := locationFromTimezone(root.FindText("timeZone", "")); location != nil {
			c.tz = location
			return location
		}
	}
	if status, err := c.SystemStatus(); err == nil {
		if location := locationFromRawTime(status["当前时间"]); location != nil {
			c.tz = location
			return location
		}
	}
	return time.Local
}

// locationFromRawTime extracts an ISO-8601 style numeric offset from values
// such as "2026-07-29T12:36:13+08:00" or "2026-07-29 12:36:13-0500".
func locationFromRawTime(raw string) *time.Location {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	if strings.HasSuffix(raw, "Z") || strings.HasSuffix(raw, "z") {
		return time.UTC
	}
	match := offsetSuffix.FindStringSubmatch(raw)
	if match == nil {
		return nil
	}
	offset := offsetSeconds(match[1], match[2], match[3], "0")
	return time.FixedZone("device", offset)
}

// locationFromTimezone parses timezone values. Hikvision normally uses a
// POSIX-style abbreviation where the sign is inverted relative to ISO-8601
// ("CST-8:00:00" means UTC+8); bare or UTC/GMT-prefixed numeric values are
// treated as ISO-8601 offsets ("+08:00" means UTC+8).
func locationFromTimezone(raw string) *time.Location {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	match := zoneName.FindStringSubmatch(raw)
	if match == nil {
		return nil
	}
	seconds := "0"
	if match[5] != "" {
		seconds = match[5]
	}
	offset := offsetSeconds(match[2], match[3], match[4], seconds)
	name := strings.ToUpper(match[1])
	if name != "" && name != "UTC" && name != "GMT" {
		// POSIX-style named zone: the sign is inverted.
		offset = -offset
	}
	return time.FixedZone("device", offset)
}

func offsetSeconds(sign, hours, minutes, seconds string) int {
	h := util.ToInt(hours, 0)
	m := util.ToInt(minutes, 0)
	s := util.ToInt(seconds, 0)
	value := h*3600 + m*60 + s
	if sign == "-" {
		value = -value
	}
	return value
}
