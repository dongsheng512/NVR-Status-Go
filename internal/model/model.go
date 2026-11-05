// Package model holds the domain types shared by the CLI, the GUI and every
// business package. The JSON tags keep the on-disk / report format compatible
// with the Python v2.3.0 implementation.
package model

import "time"

// Verdict strings used across records and health.
const (
	StatusOK      = "正常"
	StatusBad     = "异常"
	StatusUnknown = "未知"
	StatusSkip    = "跳过"
	StatusWarn    = "警告"
)

// Device is one NVR entry in a profile.
type Device struct {
	Name     string `json:"name"`
	IP       string `json:"ip"`
	Port     int    `json:"port"`
	Username string `json:"username"`
	Password string `json:"password,omitempty"`
	SSL      bool   `json:"ssl"`
}

// ScanOptions mirrors the Python `scan_options` schema. Always start from
// DefaultScanOptions() and unmarshal over it so omitted fields keep their
// defaults (0 is a meaningful value for silence_db and busy_start).
type ScanOptions struct {
	Lookback      int    `json:"lookback"`      // minutes
	LookbackUnit  string `json:"lookback_unit"` // minute|hour|day, GUI display only
	NoSearch      bool   `json:"no_search"`
	Workers       int    `json:"workers"`
	DeviceWorkers int    `json:"device_workers"`

	DeepAVCheck bool    `json:"deep_av_check"`
	AVSeconds   int     `json:"av_seconds"`
	AVWorkers   int     `json:"av_workers"`
	AVLimit     int     `json:"av_limit"` // 0 = all
	SilenceDB   float64 `json:"silence_db"`
	BusyStart   int     `json:"busy_start"`
	BusyEnd     int     `json:"busy_end"`
	BusyDaysAgo int     `json:"busy_days_ago"`
	AVSave      bool    `json:"av_save"`
	AVSaveRoot  string  `json:"av_save_root"`

	// CLI-only filters; GUI does not expose them (kept out of profiles.json).
	AVChannels []int       `json:"av_channels,omitempty"`
	AVAt       []time.Time `json:"av_at,omitempty"`
}

// DefaultScanOptions returns the v2 defaults.
func DefaultScanOptions() ScanOptions {
	return ScanOptions{
		Lookback:      60,
		LookbackUnit:  "minute",
		Workers:       8,
		DeviceWorkers: 2,
		AVSeconds:     6,
		AVWorkers:     2,
		SilenceDB:     -80,
		BusyStart:     10,
		BusyEnd:       18,
	}
}

// Profile is one configuration profile.
type Profile struct {
	Name        string      `json:"name"`
	Devices     []Device    `json:"devices"`
	Default     int         `json:"default"`
	ScanOptions ScanOptions `json:"scan_options"`
}

// Store is the profiles.json root.
type Store struct {
	Version       int                 `json:"version"`
	ActiveProfile string              `json:"active_profile"`
	Profiles      map[string]*Profile `json:"profiles"`
}

// Camera is one input-proxy camera channel.
type Camera struct {
	ID          string `json:"id"`
	Name        string `json:"名称"`
	IP          string `json:"IP"`
	Model       string `json:"型号"`
	Online      string `json:"在线"`   // "true" | "false" | "unknown"
	DetectState string `json:"检测状态"` // chanDetectResult raw value
}

// Record is one recording track; this is the row shown by CLI and GUI.
type Record struct {
	TrackID string `json:"track_id"`

	Channel string `json:"通道"`
	Name    string `json:"名称"`
	Online  string `json:"在线"`

	RecordingEnabled  *bool  `json:"已启用录像,omitempty"`
	RecordingMode     string `json:"录像模式,omitempty"`
	RecordingSchedule string `json:"录像时段,omitempty"`
	HasAudio          *bool  `json:"录像含音频,omitempty"`

	DiskStatus string `json:"落盘状态"`
	DiskDetail string `json:"落盘详情"`

	PlaybackURI string     `json:"playback_uri,omitempty"`
	SegStart    *time.Time `json:"seg_start,omitempty"`
	SegEnd      *time.Time `json:"seg_end,omitempty"`

	VideoProbe   string `json:"视频抽检"`
	AudioProbe   string `json:"音频抽检"`
	ProbeDetail  string `json:"抽检详情"`
	ProbeWindow  string `json:"抽检时段,omitempty"`
	ProbeInstant string `json:"抽检时刻,omitempty"`

	RecordingOK string `json:"录像是否正常,omitempty"` // 正常/异常/未知/未配置/跳过

	VideoCodec   string   `json:"video_codec,omitempty"`
	AudioCodec   string   `json:"audio_codec,omitempty"`
	Resolution   string   `json:"resolution,omitempty"`
	MeanVolumeDB *float64 `json:"mean_volume_db,omitempty"`
	SavePath     string   `json:"保存路径,omitempty"`
}

// Drive is one physical disk of the NVR.
type Drive struct {
	Name         string `json:"盘符"`
	CapacityTB   string `json:"容量TB"`
	FreeTB       string `json:"剩余空间TB"`
	UsedTB       string `json:"已用空间TB"`
	Usage        string `json:"使用率"`
	Status       string `json:"状态"`
	Type         string `json:"类型"`
	Property     string `json:"属性"`
	Manufacturer string `json:"制造商"`
}

// DiskOverwrite is the loop-recording (循环覆盖) verdict.
type DiskOverwrite struct {
	Enabled *bool  `json:"enabled"`
	Label   string `json:"label"`
	Detail  string `json:"detail"`
	Source  string `json:"source,omitempty"`
}

// HealthStats is the counters block of a health summary.
type HealthStats struct {
	ChannelTotal      int    `json:"通道总数"`
	PlanConfigured    int    `json:"计划已配置"`
	PlanMissing       int    `json:"计划未配置"`
	RecordingOK       int    `json:"录像正常"`
	RecordingBad      int    `json:"录像异常"`
	RecordingUnknown  int    `json:"录像未知"`
	RecordingSkip     int    `json:"录像跳过"`
	HasAudio          int    `json:"含音频"`
	NoAudio           int    `json:"不含音频"`
	AudioUnknown      int    `json:"音频未知"`
	DiskOK            int    `json:"落盘正常"`
	DiskBad           int    `json:"落盘异常"`
	DiskUnknown       int    `json:"落盘未知"`
	DiskSkip          int    `json:"落盘跳过"`
	DiskUsageUnknown  int    `json:"硬盘使用率未知"`
	DiskChecked       bool   `json:"落盘已检查"`
	RecordingChecked  bool   `json:"录像已检查"`
	VideoProbeOK      int    `json:"视频抽检正常"`
	VideoProbeBad     int    `json:"视频抽检异常"`
	AudioProbeOK      int    `json:"音频抽检正常"`
	AudioProbeBad     int    `json:"音频抽检异常"`
	AudioProbeWarn    int    `json:"音频抽检警告"`
	AudioProbeUnknown int    `json:"音频抽检未知"`
	Online            int    `json:"摄像头在线"`
	Offline           int    `json:"摄像头离线"`
	Unconfirmed       int    `json:"摄像头状态未确认"`
	DetectBad         int    `json:"通道检测异常"`
	CameraTotal       int    `json:"摄像头总数"`
	DeepAV            bool   `json:"深度抽检"`
	OverwriteLabel    string `json:"循环覆盖"`
	OverwriteEnabled  *bool  `json:"循环覆盖已开启"`
}

// HealthSummary is what health.Check returns.
type HealthSummary struct {
	Status   string      `json:"健康状态"`
	Warnings []string    `json:"预警信息"`
	Stats    HealthStats `json:"统计"`
}

// Alarm is one active alarm.
type Alarm struct {
	Type        string `json:"类型,omitempty"`
	Disk        string `json:"盘符,omitempty"`
	Description string `json:"描述,omitempty"`
	Level       string `json:"level,omitempty"`
	Time        string `json:"time,omitempty"`
}

// Report is the unified scan result shared by CLI and GUI.
type Report struct {
	DeviceName    string            `json:"device_name"`
	IP            string            `json:"ip"`
	Info          map[string]string `json:"info"`
	SysStatus     map[string]string `json:"sys_status"`
	Health        HealthSummary     `json:"health"`
	Alarms        []Alarm           `json:"alarms"`
	Cameras       []Camera          `json:"cameras"`
	Records       []Record          `json:"records"`
	Drives        []Drive           `json:"drives"`
	DiskOverwrite DiskOverwrite     `json:"disk_overwrite"`

	LookbackMinutes    int    `json:"lookback_minutes"`
	DeepAVCheck        bool   `json:"deep_av_check"`
	AVSeconds          int    `json:"av_seconds"`
	AVWorkers          int    `json:"av_workers"`
	AVChannels         []int  `json:"av_channels,omitempty"`
	BusyStartHour      int    `json:"busy_start_hour"`
	BusyEndHour        int    `json:"busy_end_hour"`
	AVSave             bool   `json:"av_save"`
	AVSaveDir          string `json:"av_save_dir,omitempty"`
	CheckDiskRecording bool   `json:"check_disk_recording"`

	Error string `json:"error,omitempty"`
}

// Progress is one progress callback message.
type Progress struct {
	Message string  `json:"message"`
	Phase   string  `json:"phase"`
	Current int     `json:"current"`
	Total   int     `json:"total"`
	Overall float64 `json:"overall"`
}
