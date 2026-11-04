package avprobe

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"nvrstatus/internal/model"
	"nvrstatus/internal/recording"
	"nvrstatus/internal/util"
)

// ---------------------------------------------------------------------------
// fake ffmpeg / ffprobe
//
// The probe only talks to the outside world through these two binaries, so
// shell scripts controlled by environment variables give us a real end-to-end
// test of command construction, exit codes and cancellation without any media
// source.

const fakeFFmpeg = `#!/bin/sh
out=""
mode="pull"
audio=0
prev=""
inp=""
for a in "$@"; do
  case "$a" in
    -vn) audio=1 ;;
    volumedetect) mode="vol" ;;
  esac
  [ "$prev" = "-i" ] && inp="$a"
  prev="$a"
  out="$a"
done
if [ -n "${FAKE_LOG:-}" ]; then
  printf '%s\n' "$*" >> "$FAKE_LOG"
fi
if [ "$mode" = "vol" ]; then
  if [ -n "${FAKE_MEAN_DB:-}" ]; then
    echo "[Parsed_volumedetect_0 @ 0x1] mean_volume: ${FAKE_MEAN_DB} dB" >&2
  fi
  exit 0
fi
if [ -n "${FAKE_SLEEP:-}" ]; then
  sleep "$FAKE_SLEEP"
fi
if [ "$audio" = "1" ]; then
  st="${FAKE_AUDIO_MODE:-ok}"
else
  st="${FAKE_VIDEO_MODE:-ok}"
fi
case "$inp" in
  *FAILME*) st="fail" ;;
esac
case "$st" in
  ok)
    dd if=/dev/zero of="$out" bs=1024 count=16 2>/dev/null
    ;;
  small)
    dd if=/dev/zero of="$out" bs=1 count=100 2>/dev/null
    ;;
  nostream)
    : > "$out"
    echo "Stream map '0:a:0' matches no streams." >&2
    ;;
  *)
    : > "$out"
    echo "Failed to open input: $inp" >&2
    ;;
esac
exit 0
`

const fakeFFprobe = `#!/bin/sh
rc="${FAKE_FFPROBE_RC:-0}"
if [ "$rc" != "0" ]; then
  echo "probe: invalid data found" >&2
  exit "$rc"
fi
has_fmt=0
for a in "$@"; do
  [ "$a" = "-show_format" ] && has_fmt=1
done
if [ "$has_fmt" = "1" ]; then
  v="${FAKE_VIDEO_JSON:-}"
  [ -z "$v" ] && v='{"streams":[]}'
  printf '%s' "$v"
else
  a="${FAKE_AUDIO_JSON:-}"
  [ -z "$a" ] && a='{"streams":[]}'
  printf '%s' "$a"
fi
exit 0
`

var (
	testClipStart = time.Date(2026, 9, 10, 6, 0, 0, 0, time.UTC)
	testNow       = time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC)
	videoOKJSON   = mustJSON(map[string]any{
		"streams": []map[string]any{
			{"codec_type": "video", "codec_name": "h264", "width": 1920, "height": 1080},
		},
	})
	audioOKJSON = mustJSON(map[string]any{
		"streams": []map[string]any{
			{"codec_type": "audio", "codec_name": "aac"},
		},
	})
)

func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return string(b)
}

type fakeTools struct {
	ffmpeg  string
	ffprobe string
	log     string
}

func installFakes(t *testing.T) fakeTools {
	t.Helper()
	dir := t.TempDir()
	ffmpeg := filepath.Join(dir, "ffmpeg")
	ffprobe := filepath.Join(dir, "ffprobe")
	if err := os.WriteFile(ffmpeg, []byte(fakeFFmpeg), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ffprobe, []byte(fakeFFprobe), 0o755); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(dir, "calls.log")
	t.Setenv("FAKE_LOG", logPath)
	t.Setenv("FAKE_VIDEO_MODE", "ok")
	t.Setenv("FAKE_AUDIO_MODE", "ok")
	t.Setenv("FAKE_MEAN_DB", "-42.5")
	t.Setenv("FAKE_VIDEO_JSON", videoOKJSON)
	t.Setenv("FAKE_AUDIO_JSON", audioOKJSON)
	t.Setenv("FAKE_FFPROBE_RC", "")
	t.Setenv("FAKE_SLEEP", "")
	return fakeTools{ffmpeg: ffmpeg, ffprobe: ffprobe, log: logPath}
}

func readCalls(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatal(err)
	}
	text := strings.TrimSpace(string(data))
	if text == "" {
		return nil
	}
	return strings.Split(text, "\n")
}

// ---------------------------------------------------------------------------
// fake recording service

type fakeRecording struct {
	searchFn    func(trackID string, start, end time.Time) recording.SearchResult
	busyFn      func(seconds int, at *time.Time) (time.Time, time.Time, string)
	retryFn     func(seconds int, avoidS, avoidE time.Time) (time.Time, time.Time, string, bool)
	searchCalls int32
	retryCalls  int32
}

func (f *fakeRecording) SearchTrackInRange(trackID string, start, end time.Time) recording.SearchResult {
	atomic.AddInt32(&f.searchCalls, 1)
	if f.searchFn != nil {
		return f.searchFn(trackID, start, end)
	}
	return foundResult(trackID, "192.168.1.64")
}

func (f *fakeRecording) PickBusyClipTimes(seconds int, at *time.Time) (time.Time, time.Time, string) {
	if f.busyFn != nil {
		return f.busyFn(seconds, at)
	}
	if at != nil {
		a := at.Local()
		return *at, at.Add(time.Duration(seconds) * time.Second),
			fmt.Sprintf("测试定点窗; 抽检点 %s", a.Format("01-02 15:04:05"))
	}
	return testClipStart, testClipStart.Add(time.Duration(seconds) * time.Second),
		"测试繁忙窗; 抽检点 09-10 14:00:00(14:00附近)"
}

func (f *fakeRecording) PickRetryClipTimes(seconds int, avoidS, avoidE time.Time) (time.Time, time.Time, string, bool) {
	atomic.AddInt32(&f.retryCalls, 1)
	if f.retryFn != nil {
		return f.retryFn(seconds, avoidS, avoidE)
	}
	s := testClipStart.Add(time.Hour)
	return s, s.Add(time.Duration(seconds) * time.Second), "测试繁忙窗; 复检点 09-10 15:00:00(换时段)", true
}

func testURI(trackID, host string) string {
	return fmt.Sprintf("rtsp://%s/Streaming/tracks/%s/?starttime=20260910T140000Z&endtime=20260910T150000Z&name=ch1&size=999999", host, trackID)
}

func foundResult(trackID, host string) recording.SearchResult {
	s := testClipStart
	e := s.Add(10 * time.Minute)
	return recording.SearchResult{
		OK:          true,
		Status:      model.StatusOK,
		Detail:      "ok",
		PlaybackURI: testURI(trackID, host),
		SegStart:    &s,
		SegEnd:      &e,
	}
}

func boolPtr(v bool) *bool { return &v }

func makeRecord(trackID, channel, name string, hasAudio *bool, enabled bool, disk string) model.Record {
	en := enabled
	if disk == "" {
		disk = model.StatusOK
	}
	return model.Record{
		TrackID:           trackID,
		Channel:           channel,
		Name:              name,
		Online:            "true",
		RecordingEnabled:  &en,
		RecordingMode:     "连续录像",
		RecordingSchedule: "1/1",
		HasAudio:          hasAudio,
		DiskStatus:        disk,
		ProbeDetail:       "未启用深度抽检",
	}
}

func newTestService(t *testing.T) (*Service, fakeTools, *fakeRecording) {
	t.Helper()
	tools := installFakes(t)
	fr := &fakeRecording{}
	svc := &Service{
		Rec:       fr,
		FFmpeg:    tools.ffmpeg,
		FFprobe:   tools.ffprobe,
		AVSeconds: 3,
		AVWorkers: 1,
		SilenceDB: -80,
		Username:  "admin",
		Password:  "b",
		Location:  time.FixedZone("CST", 8*3600),
		Now:       func() time.Time { return testNow },
	}
	return svc, tools, fr
}

// ---------------------------------------------------------------------------
// end-to-end probe cases

func TestRunVideoAndAudioOKSplitsCommands(t *testing.T) {
	svc, tools, _ := newTestService(t)
	recs := []model.Record{makeRecord("101", "1", "前门", boolPtr(true), true, "")}

	if err := svc.Run(recs); err != nil {
		t.Fatal(err)
	}
	r := recs[0]
	if r.VideoProbe != model.StatusOK || r.AudioProbe != model.StatusOK {
		t.Fatalf("status = %s/%s, detail=%s", r.VideoProbe, r.AudioProbe, r.ProbeDetail)
	}
	if r.VideoCodec != "h264" || r.AudioCodec != "aac" || r.Resolution != "1920x1080" {
		t.Fatalf("codecs = %q/%q/%q", r.VideoCodec, r.AudioCodec, r.Resolution)
	}
	if r.MeanVolumeDB == nil || *r.MeanVolumeDB != -42.5 {
		t.Fatalf("mean = %v", r.MeanVolumeDB)
	}
	if !strings.Contains(r.ProbeDetail, "短时抽检OK@09-10 14:00:00") ||
		!strings.Contains(r.ProbeDetail, "1920x1080") ||
		!strings.Contains(r.ProbeDetail, "aac") {
		t.Fatalf("detail = %q", r.ProbeDetail)
	}
	if !strings.Contains(r.ProbeWindow, "测试繁忙窗") {
		t.Fatalf("window = %q", r.ProbeWindow)
	}

	lines := readCalls(t, tools.log)
	var videoCmds, audioCmds []string
	for _, line := range lines {
		if strings.Contains(line, "-map 0:v:0") {
			videoCmds = append(videoCmds, line)
		}
		if strings.Contains(line, "-map 0:a:0") {
			audioCmds = append(audioCmds, line)
		}
	}
	if len(videoCmds) == 0 || len(audioCmds) == 0 {
		t.Fatalf("missing commands: video=%d audio=%d", len(videoCmds), len(audioCmds))
	}
	for _, line := range videoCmds {
		if strings.Contains(line, "-vn") {
			t.Fatalf("video command must not contain -vn: %s", line)
		}
	}
	for _, line := range audioCmds {
		if !strings.Contains(line, "-vn") {
			t.Fatalf("audio command must contain -vn: %s", line)
		}
	}
	volDetected := false
	for _, line := range lines {
		if strings.Contains(line, "volumedetect") {
			volDetected = true
		}
	}
	if !volDetected {
		t.Fatal("no volumedetect command")
	}
}

func TestAudioMissingTrackAnomalyWhenConfigured(t *testing.T) {
	svc, _, _ := newTestService(t)
	t.Setenv("FAKE_AUDIO_MODE", "nostream")
	recs := []model.Record{makeRecord("101", "1", "cam101", boolPtr(true), true, "")}

	if err := svc.Run(recs); err != nil {
		t.Fatal(err)
	}
	if recs[0].VideoProbe != model.StatusOK {
		t.Fatalf("video = %s", recs[0].VideoProbe)
	}
	if recs[0].AudioProbe != model.StatusBad {
		t.Fatalf("audio = %s, want 异常", recs[0].AudioProbe)
	}
	if !strings.Contains(recs[0].ProbeDetail, "无音轨") {
		t.Fatalf("detail = %q", recs[0].ProbeDetail)
	}
}

func TestAudioMissingTrackSkipWhenNotConfigured(t *testing.T) {
	svc, _, _ := newTestService(t)
	t.Setenv("FAKE_AUDIO_MODE", "nostream")
	recs := []model.Record{makeRecord("101", "1", "cam101", boolPtr(false), true, "")}

	if err := svc.Run(recs); err != nil {
		t.Fatal(err)
	}
	if recs[0].VideoProbe != model.StatusOK {
		t.Fatalf("video = %s", recs[0].VideoProbe)
	}
	if recs[0].AudioProbe != model.StatusSkip {
		t.Fatalf("audio = %s, want 跳过", recs[0].AudioProbe)
	}
	if !strings.Contains(recs[0].ProbeDetail, "配置未开音频") {
		t.Fatalf("detail = %q", recs[0].ProbeDetail)
	}
}

func TestZeroSilenceDBUsesPythonDefault(t *testing.T) {
	svc, _, _ := newTestService(t)
	svc.SilenceDB = 0 // zero-value Service: mean -42.5 must not be "silent"
	recs := []model.Record{makeRecord("101", "1", "cam101", boolPtr(true), true, "")}
	if err := svc.Run(recs); err != nil {
		t.Fatal(err)
	}
	if recs[0].AudioProbe != model.StatusOK {
		t.Fatalf("audio = %s, want 正常", recs[0].AudioProbe)
	}
}

func TestAudioSilenceIsWarning(t *testing.T) {
	svc, _, _ := newTestService(t)
	t.Setenv("FAKE_MEAN_DB", "-95.0")
	recs := []model.Record{makeRecord("101", "1", "cam101", boolPtr(true), true, "")}

	if err := svc.Run(recs); err != nil {
		t.Fatal(err)
	}
	if recs[0].AudioProbe != model.StatusWarn {
		t.Fatalf("audio = %s, want 警告", recs[0].AudioProbe)
	}
	if !strings.Contains(recs[0].ProbeDetail, "静音") {
		t.Fatalf("detail = %q", recs[0].ProbeDetail)
	}
}

func TestAudioPullFailureIsUnknown(t *testing.T) {
	svc, _, _ := newTestService(t)
	t.Setenv("FAKE_AUDIO_MODE", "fail")
	recs := []model.Record{makeRecord("101", "1", "cam101", boolPtr(true), true, "")}

	if err := svc.Run(recs); err != nil {
		t.Fatal(err)
	}
	if recs[0].VideoProbe != model.StatusOK {
		t.Fatalf("video = %s", recs[0].VideoProbe)
	}
	if recs[0].AudioProbe != model.StatusUnknown {
		t.Fatalf("audio = %s, want 未知", recs[0].AudioProbe)
	}
	if !strings.Contains(recs[0].ProbeDetail, "音频未确认") {
		t.Fatalf("detail = %q", recs[0].ProbeDetail)
	}
}

func TestVideoPullFailureMasksCredentials(t *testing.T) {
	svc, _, _ := newTestService(t)
	svc.Password = "P@ss w0rd!"
	t.Setenv("FAKE_VIDEO_MODE", "fail")
	rec := makeRecord("101", "1", "cam101", boolPtr(false), true, "")
	clipE := testClipStart.Add(3 * time.Second)
	if err := svc.ProbeRecord(&rec, testClipStart, clipE, "测试窗; 抽检点 09-10 14:00:00"); err != nil {
		t.Fatal(err)
	}
	if rec.VideoProbe != model.StatusBad {
		t.Fatalf("video = %s, want 异常", rec.VideoProbe)
	}
	if !strings.Contains(rec.ProbeDetail, "短时拉流失败") {
		t.Fatalf("detail = %q", rec.ProbeDetail)
	}
	if strings.Contains(rec.ProbeDetail, "P@ss w0rd!") || strings.Contains(rec.ProbeDetail, "P%40ss") {
		t.Fatalf("password leaked: %q", rec.ProbeDetail)
	}
	if !strings.Contains(rec.ProbeDetail, "***:***@") {
		t.Fatalf("credentials not masked: %q", rec.ProbeDetail)
	}
}

func TestLowResolutionIsAnomalyAndAudioSkipped(t *testing.T) {
	svc, tools, _ := newTestService(t)
	t.Setenv("FAKE_VIDEO_JSON", `{"streams":[{"codec_type":"video","codec_name":"h264","width":64,"height":48}]}`)
	rec := makeRecord("101", "1", "cam101", boolPtr(true), true, "")
	clipE := testClipStart.Add(3 * time.Second)
	if err := svc.ProbeRecord(&rec, testClipStart, clipE, "测试窗"); err != nil {
		t.Fatal(err)
	}
	if rec.VideoProbe != model.StatusBad || !strings.Contains(rec.ProbeDetail, "分辨率异常") {
		t.Fatalf("video = %s detail=%q", rec.VideoProbe, rec.ProbeDetail)
	}
	if rec.AudioProbe != model.StatusSkip {
		t.Fatalf("audio = %s, want 跳过", rec.AudioProbe)
	}
	for _, line := range readCalls(t, tools.log) {
		if strings.Contains(line, "-map 0:a:0") {
			t.Fatalf("should not pull audio after bad video: %s", line)
		}
	}
}

func TestNoVideoStreamIsAnomaly(t *testing.T) {
	svc, _, _ := newTestService(t)
	t.Setenv("FAKE_VIDEO_JSON", `{"streams":[]}`)
	rec := makeRecord("101", "1", "cam101", boolPtr(true), true, "")
	clipE := testClipStart.Add(3 * time.Second)
	if err := svc.ProbeRecord(&rec, testClipStart, clipE, "测试窗"); err != nil {
		t.Fatal(err)
	}
	if rec.VideoProbe != model.StatusBad || rec.ProbeDetail != "无视频轨" {
		t.Fatalf("video = %s detail=%q", rec.VideoProbe, rec.ProbeDetail)
	}
	if rec.AudioProbe != model.StatusSkip {
		t.Fatalf("audio = %s, want 跳过", rec.AudioProbe)
	}
}

func TestFFprobeFailureIsAnomaly(t *testing.T) {
	svc, _, _ := newTestService(t)
	t.Setenv("FAKE_FFPROBE_RC", "1")
	rec := makeRecord("101", "1", "cam101", boolPtr(true), true, "")
	clipE := testClipStart.Add(3 * time.Second)
	if err := svc.ProbeRecord(&rec, testClipStart, clipE, "测试窗"); err != nil {
		t.Fatal(err)
	}
	if rec.VideoProbe != model.StatusBad || rec.ProbeDetail != "ffprobe解析失败" {
		t.Fatalf("video = %s detail=%q", rec.VideoProbe, rec.ProbeDetail)
	}
}

func TestMissingToolsReturnsUnknown(t *testing.T) {
	svc, _, _ := newTestService(t)
	svc.FFmpeg = ""
	svc.FFprobe = ""
	rec := makeRecord("101", "1", "cam101", boolPtr(true), true, "")
	rec.PlaybackURI = testURI("101", "192.168.1.64")
	if err := svc.ProbeRecord(&rec, testClipStart, testClipStart.Add(3*time.Second), "测试窗"); err != nil {
		t.Fatal(err)
	}
	if rec.VideoProbe != model.StatusUnknown || rec.AudioProbe != model.StatusUnknown {
		t.Fatalf("status = %s/%s", rec.VideoProbe, rec.AudioProbe)
	}
	if rec.ProbeDetail != "本机未安装 ffmpeg/ffprobe" {
		t.Fatalf("detail = %q", rec.ProbeDetail)
	}
}

func TestMissingBinaryIsGenericUnknown(t *testing.T) {
	svc, _, _ := newTestService(t)
	svc.FFmpeg = filepath.Join(t.TempDir(), "no-such-ffmpeg")
	rec := makeRecord("101", "1", "cam101", boolPtr(true), true, "")
	rec.PlaybackURI = testURI("101", "192.168.1.64")
	if err := svc.ProbeRecord(&rec, testClipStart, testClipStart.Add(3*time.Second), "测试窗"); err != nil {
		t.Fatal(err)
	}
	if rec.VideoProbe != model.StatusUnknown || rec.AudioProbe != model.StatusUnknown {
		t.Fatalf("status = %s/%s", rec.VideoProbe, rec.AudioProbe)
	}
	if !strings.Contains(rec.ProbeDetail, "抽检异常") {
		t.Fatalf("detail = %q", rec.ProbeDetail)
	}
}

func TestProbeChannelAtSkipsWithoutURI(t *testing.T) {
	svc, tools, fr := newTestService(t)
	fr.searchFn = func(trackID string, start, end time.Time) recording.SearchResult {
		return recording.SearchResult{Detail: "繁忙时段无录像片段"}
	}
	rec := makeRecord("101", "1", "cam101", boolPtr(true), true, "")
	rec.PlaybackURI = ""
	clipE := testClipStart.Add(3 * time.Second)
	res, err := svc.probeChannelAt(&rec, testClipStart, clipE, "测试窗")
	if err != nil {
		t.Fatal(err)
	}
	if res.VideoProbe != model.StatusSkip || res.AudioProbe != model.StatusSkip {
		t.Fatalf("status = %s/%s", res.VideoProbe, res.AudioProbe)
	}
	if !strings.Contains(res.Detail, "无录像片段") {
		t.Fatalf("detail = %q", res.Detail)
	}
	if len(readCalls(t, tools.log)) != 0 {
		t.Fatal("ffmpeg must not be called without a playback URI")
	}
}

// ---------------------------------------------------------------------------
// candidate filtering / limits / AVChannels

func TestCandidateFilteringAndLimit(t *testing.T) {
	svc, _, _ := newTestService(t)
	svc.AVLimit = 2
	recs := []model.Record{
		makeRecord("101", "1", "cam1", boolPtr(true), true, ""),
		makeRecord("201", "2", "cam2", boolPtr(true), true, ""),
		makeRecord("301", "3", "cam3", boolPtr(true), true, ""),
		makeRecord("401", "4", "cam4", boolPtr(true), false, ""),
		makeRecord("501", "5", "cam5", boolPtr(true), true, model.StatusBad),
	}
	if err := svc.Run(recs); err != nil {
		t.Fatal(err)
	}
	byCh := map[string]*model.Record{}
	for i := range recs {
		byCh[recs[i].Channel] = &recs[i]
	}
	if byCh["4"].ProbeDetail != "未配置录像" || byCh["4"].VideoProbe != model.StatusSkip {
		t.Fatalf("ch4 = %+v", byCh["4"])
	}
	if byCh["5"].ProbeDetail != "近期无录像/未知,跳过拉流" || byCh["5"].VideoProbe != model.StatusSkip {
		t.Fatalf("ch5 = %+v", byCh["5"])
	}
	if byCh["3"].ProbeDetail != "超出抽检路数上限(2)" {
		t.Fatalf("ch3 detail = %q", byCh["3"].ProbeDetail)
	}
	if byCh["1"].VideoProbe != model.StatusOK || byCh["2"].VideoProbe != model.StatusOK {
		t.Fatalf("first two should be probed: %s/%s", byCh["1"].VideoProbe, byCh["2"].VideoProbe)
	}
}

func TestInvalidChannelSkipped(t *testing.T) {
	svc, tools, _ := newTestService(t)
	recs := []model.Record{makeRecord("未知", "未知", "cam", boolPtr(true), true, "")}
	if err := svc.Run(recs); err != nil {
		t.Fatal(err)
	}
	if recs[0].VideoProbe != model.StatusSkip || recs[0].ProbeDetail != "通道号无效" {
		t.Fatalf("record = %+v", recs[0])
	}
	if len(readCalls(t, tools.log)) != 0 {
		t.Fatal("invalid channel must not be probed")
	}
}

func TestAVChannelsFiltersProbing(t *testing.T) {
	svc, tools, _ := newTestService(t)
	svc.AVChannels = []int{31, 64}
	recs := []model.Record{
		makeRecord("3101", "31", "前端相机-1", boolPtr(true), true, ""),
		makeRecord("6401", "64", "前端相机-2", boolPtr(true), true, ""),
		makeRecord("6501", "65", "cam65", boolPtr(true), true, ""),
	}
	if err := svc.Run(recs); err != nil {
		t.Fatal(err)
	}
	byCh := map[string]*model.Record{}
	for i := range recs {
		byCh[recs[i].Channel] = &recs[i]
	}
	if byCh["31"].VideoProbe != model.StatusOK || byCh["64"].VideoProbe != model.StatusOK {
		t.Fatalf("listed channels not probed: %s/%s", byCh["31"].VideoProbe, byCh["64"].VideoProbe)
	}
	if byCh["65"].VideoProbe != model.StatusSkip || byCh["65"].ProbeDetail != "不在指定抽检通道列表" {
		t.Fatalf("ch65 = %+v", byCh["65"])
	}
	joined := strings.Join(readCalls(t, tools.log), " ")
	if strings.Contains(joined, "/Streaming/tracks/6501/") {
		t.Fatal("channel outside filter was pulled")
	}
}

func TestAVChannelsAppliesBeforeLimit(t *testing.T) {
	svc, _, _ := newTestService(t)
	svc.AVChannels = []int{31, 64}
	svc.AVLimit = 1
	recs := []model.Record{
		makeRecord("3101", "31", "cam31", boolPtr(true), true, ""),
		makeRecord("6401", "64", "cam64", boolPtr(true), true, ""),
		makeRecord("6501", "65", "cam65", boolPtr(true), true, ""),
	}
	if err := svc.Run(recs); err != nil {
		t.Fatal(err)
	}
	byCh := map[string]*model.Record{}
	for i := range recs {
		byCh[recs[i].Channel] = &recs[i]
	}
	if byCh["31"].VideoProbe != model.StatusOK {
		t.Fatalf("ch31 = %+v", byCh["31"])
	}
	if byCh["64"].ProbeDetail != "超出抽检路数上限(1)" {
		t.Fatalf("ch64 detail = %q", byCh["64"].ProbeDetail)
	}
	if byCh["65"].ProbeDetail != "不在指定抽检通道列表" {
		t.Fatalf("ch65 detail = %q", byCh["65"].ProbeDetail)
	}
}

func TestAVChannelsKeepsRealProblemReason(t *testing.T) {
	svc, tools, _ := newTestService(t)
	svc.AVChannels = []int{31, 64}
	recs := []model.Record{
		makeRecord("3101", "31", "cam31", boolPtr(true), false, ""),
		makeRecord("6401", "64", "cam64", boolPtr(true), true, model.StatusBad),
	}
	if err := svc.Run(recs); err != nil {
		t.Fatal(err)
	}
	if recs[0].ProbeDetail != "未配置录像" {
		t.Fatalf("ch31 detail = %q", recs[0].ProbeDetail)
	}
	if recs[1].ProbeDetail != "近期无录像/未知,跳过拉流" {
		t.Fatalf("ch64 detail = %q", recs[1].ProbeDetail)
	}
	if len(readCalls(t, tools.log)) != 0 {
		t.Fatal("no candidate should have been probed")
	}
}

func TestAVChannelsInvalidChannelGetsFilterReason(t *testing.T) {
	svc, tools, _ := newTestService(t)
	svc.AVChannels = []int{31}
	recs := []model.Record{makeRecord("未知", "未知", "cam", boolPtr(true), true, "")}
	if err := svc.Run(recs); err != nil {
		t.Fatal(err)
	}
	if recs[0].ProbeDetail != "不在指定抽检通道列表" {
		t.Fatalf("detail = %q", recs[0].ProbeDetail)
	}
	if len(readCalls(t, tools.log)) != 0 {
		t.Fatal("invalid channel should not be probed")
	}
}

// ---------------------------------------------------------------------------
// retry

func TestRetryRecoversAndAnnotates(t *testing.T) {
	svc, _, fr := newTestService(t)
	fr.searchFn = func(trackID string, start, end time.Time) recording.SearchResult {
		if atomic.LoadInt32(&fr.searchCalls) == 1 {
			return foundResult(trackID, "FAILME")
		}
		return foundResult(trackID, "192.168.1.64")
	}
	recs := []model.Record{makeRecord("101", "1", "前门", boolPtr(true), true, "")}
	if err := svc.Run(recs); err != nil {
		t.Fatal(err)
	}
	if got := atomic.LoadInt32(&fr.searchCalls); got != 2 {
		t.Fatalf("search calls = %d, want 2", got)
	}
	if recs[0].VideoProbe != model.StatusOK {
		t.Fatalf("video = %s, want 正常 (%s)", recs[0].VideoProbe, recs[0].ProbeDetail)
	}
	if !strings.Contains(recs[0].ProbeDetail, "复检通过,初检失败") {
		t.Fatalf("detail = %q", recs[0].ProbeDetail)
	}
}

func TestRetryStillFailingAnnotates(t *testing.T) {
	svc, _, fr := newTestService(t)
	fr.searchFn = func(trackID string, start, end time.Time) recording.SearchResult {
		return foundResult(trackID, "FAILME")
	}
	recs := []model.Record{makeRecord("101", "1", "前门", boolPtr(true), true, "")}
	if err := svc.Run(recs); err != nil {
		t.Fatal(err)
	}
	if recs[0].VideoProbe != model.StatusBad {
		t.Fatalf("video = %s, want 异常", recs[0].VideoProbe)
	}
	if !strings.Contains(recs[0].ProbeDetail, "复检@") || !strings.Contains(recs[0].ProbeDetail, "仍异常") {
		t.Fatalf("detail = %q", recs[0].ProbeDetail)
	}
}

// ---------------------------------------------------------------------------
// cancellation

func TestRunCancellationKillsProcessAndReturnsErrCancelled(t *testing.T) {
	svc, tools, _ := newTestService(t)
	t.Setenv("FAKE_SLEEP", "30")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	svc.Ctx = ctx
	recs := []model.Record{makeRecord("101", "1", "cam101", boolPtr(true), true, "")}
	done := make(chan error, 1)
	go func() { done <- svc.Run(recs) }()

	// Wait until the fake ffmpeg has actually started.
	deadline := time.Now().Add(5 * time.Second)
	for {
		if len(readCalls(t, tools.log)) > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("fake ffmpeg did not start")
		}
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	select {
	case err := <-done:
		if err != util.ErrCancelled {
			t.Fatalf("err = %v, want ErrCancelled", err)
		}
	case <-time.After(8 * time.Second):
		t.Fatal("Run did not return after cancellation")
	}
}

// ---------------------------------------------------------------------------
// RTSP candidates / :554 fallback

func TestRTSPCandidatesAndPortFallback(t *testing.T) {
	svc := &Service{
		Location: time.FixedZone("CST", 8*3600),
		Now:      func() time.Time { return testNow },
		Username: "admin",
		Password: "P@ss w0rd!",
	}
	segS := testClipStart
	segE := segS.Add(10 * time.Minute)
	clipS := segS
	clipE := clipS.Add(6 * time.Second)
	uri := "rtsp://192.168.1.64:80/Streaming/tracks/101/?starttime=20260910T140000Z&endtime=20260910T150000Z&name=ch1"

	cands := svc.buildShortRTSPCandidates(uri, &segS, &segE, 6, &clipS, &clipE)
	labels := make([]string, len(cands))
	byLabel := map[string]string{}
	urls := map[string]bool{}
	for i, c := range cands {
		labels[i] = c.Label
		byLabel[c.Label] = c.URL
		if urls[c.URL] {
			t.Fatalf("duplicate candidate URL %s", c.URL)
		}
		urls[c.URL] = true
	}
	for _, base := range []string{"short/local", "short/utc", "original"} {
		if _, ok := byLabel[base]; !ok {
			t.Fatalf("missing %s in %v", base, labels)
		}
		if _, ok := byLabel[base+"@554"]; !ok {
			t.Fatalf("missing %s@554 in %v", base, labels)
		}
		alt, ok := swapRTSPPort(byLabel[base], 554)
		if !ok || alt != byLabel[base+"@554"] {
			t.Fatalf("%s@554 mismatch: %q vs %q", base, alt, byLabel[base+"@554"])
		}
	}
	if !strings.Contains(byLabel["short/local"], "starttime=20260910T140000Z") {
		t.Fatalf("local rewrite = %s", byLabel["short/local"])
	}
	if strings.Contains(byLabel["short/local"], "size=") {
		t.Fatalf("size parameter not stripped: %s", byLabel["short/local"])
	}
	wantAuth := "admin:" + escapeUserInfo("P@ss w0rd!") + "@"
	if !strings.Contains(byLabel["short/local"], wantAuth) {
		t.Fatalf("auth not injected/encoded: %s", byLabel["short/local"])
	}

	ordered := sortCandidates(cands)
	if ordered[0].Label != "original" || ordered[1].Label != "short/local" || ordered[2].Label != "short/utc" {
		t.Fatalf("order head = %v", []string{ordered[0].Label, ordered[1].Label, ordered[2].Label})
	}
	for _, c := range ordered[3:] {
		if !strings.HasSuffix(c.Label, "@554") {
			t.Fatalf("@554 not last: %v", ordered)
		}
	}
}

func TestRTSPCandidatesNoExplicitPort(t *testing.T) {
	svc := &Service{
		Location: time.FixedZone("CST", 8*3600),
		Now:      func() time.Time { return testNow },
	}
	uri := "rtsp://192.168.1.64/Streaming/tracks/101/?starttime=20260910T140000Z&endtime=20260910T150000Z"
	cands := svc.buildShortRTSPCandidates(uri, nil, nil, 6, nil, nil)
	if len(cands) == 0 {
		t.Fatal("no candidates")
	}
	if cands[len(cands)-1].Label != "original" {
		t.Fatalf("last label = %s", cands[len(cands)-1].Label)
	}
	for _, c := range cands {
		if strings.HasSuffix(c.Label, "@554") {
			t.Fatalf("unexpected @554 variant: %s", c.Label)
		}
	}
}

func TestSwapRTSPPort(t *testing.T) {
	out, ok := swapRTSPPort("rtsp://192.168.1.64:80/Streaming/tracks/101/?a=1", 554)
	if !ok || !strings.HasPrefix(out, "rtsp://192.168.1.64:554/Streaming/tracks/101/") {
		t.Fatalf("out = %q ok=%v", out, ok)
	}
	out, ok = swapRTSPPort("rtsp://admin:pw@[fe80::1]:80/Streaming/tracks/101/", 554)
	if !ok || !strings.HasPrefix(out, "rtsp://admin:pw@[fe80::1]:554/") {
		t.Fatalf("ipv6 out = %q ok=%v", out, ok)
	}
	if _, ok := swapRTSPPort("rtsp://admin:pw@[fe80::1]/Streaming", 554); ok {
		t.Fatal("IPv6 without port should return false")
	}
	if _, ok := swapRTSPPort("rtsp://192.168.1.64/Streaming", 554); ok {
		t.Fatal("no explicit port should return false")
	}
	if _, ok := swapRTSPPort("rtsp://192.168.1.64:554/Streaming", 554); ok {
		t.Fatal("already 554 should return false")
	}
	if _, ok := swapRTSPPort("http://192.168.1.64:80/ISAPI", 554); ok {
		t.Fatal("non-RTSP should return false")
	}
}

func TestDetectRTSPTimeMode(t *testing.T) {
	svc := &Service{Location: time.FixedZone("CST", 8*3600)}
	seg := testClipStart // 06:00 UTC == 14:00 CST
	if got := svc.detectRTSPTimeMode("20260910T140000Z", &seg); got != "local" {
		t.Fatalf("mode = %s, want local", got)
	}
	if got := svc.detectRTSPTimeMode("20260910T060000Z", &seg); got != "utc" {
		t.Fatalf("mode = %s, want utc", got)
	}
	if got := svc.detectRTSPTimeMode("", nil); got != "local" {
		t.Fatalf("mode = %s, want local", got)
	}
}

func TestComputeClipWindow(t *testing.T) {
	svc := &Service{Now: func() time.Time { return testNow }}
	uriS := testClipStart
	uriE := uriS.Add(10 * time.Minute)
	cs, ce := svc.computeClipWindow(uriS, uriE, 6, nil, nil)
	if !ce.Equal(uriE.Add(-2*time.Second)) || !cs.Equal(ce.Add(-6*time.Second)) {
		t.Fatalf("no clip = %v..%v", cs, ce)
	}
	clipS := uriS
	clipE := uriS.Add(6 * time.Second)
	cs, ce = svc.computeClipWindow(uriS, uriE, 6, &clipS, &clipE)
	if !cs.Equal(clipS) || !ce.Equal(clipE) {
		t.Fatalf("explicit clip = %v..%v", cs, ce)
	}
}

// ---------------------------------------------------------------------------
// saving clips

func TestSavePathNaming(t *testing.T) {
	svc, _, _ := newTestService(t)
	svc.AVSave = true
	svc.AVSaveRoot = t.TempDir()
	recs := []model.Record{makeRecord("101", "1", "前门", boolPtr(true), true, "")}
	if err := svc.Run(recs); err != nil {
		t.Fatal(err)
	}
	path := recs[0].SavePath
	if path == "" {
		t.Fatalf("SavePath empty; detail=%s", recs[0].ProbeDetail)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("saved file missing: %v", err)
	}
	base := filepath.Base(path)
	ok, _ := regexp.MatchString(`^ch1_前门_track101_140000\.mkv$`, base)
	if !ok {
		t.Fatalf("filename = %q", base)
	}
	dir := filepath.Dir(path)
	if !regexp.MustCompile(`^\d{8}_\d{6}$`).MatchString(filepath.Base(dir)) {
		t.Fatalf("run dir = %q", dir)
	}
	if svc.AVSaveDir() != dir {
		t.Fatalf("AVSaveDir = %q, want %q", svc.AVSaveDir(), dir)
	}
	st, err := os.Stat(path)
	if err != nil || st.Size() < 1024 {
		t.Fatalf("saved size = %v err=%v", st, err)
	}
}

// ---------------------------------------------------------------------------
// fixed-instant mode

func TestMultiInstantUsesBestDefiniteAndRecordsTrail(t *testing.T) {
	svc, _, fr := newTestService(t)
	t1 := testClipStart
	t2 := testClipStart.Add(2 * time.Hour)
	svc.AVAt = []time.Time{t1, t2}
	fr.searchFn = func(trackID string, start, end time.Time) recording.SearchResult {
		if atomic.LoadInt32(&fr.searchCalls) == 1 {
			return foundResult(trackID, "FAILME")
		}
		return foundResult(trackID, "192.168.1.64")
	}
	recs := []model.Record{makeRecord("3101", "31", "cam31", boolPtr(true), true, "")}
	if err := svc.Run(recs); err != nil {
		t.Fatal(err)
	}
	r := recs[0]
	if r.VideoProbe != model.StatusOK || r.AudioProbe != model.StatusOK {
		t.Fatalf("status = %s/%s detail=%s", r.VideoProbe, r.AudioProbe, r.ProbeDetail)
	}
	if !strings.Contains(r.ProbeDetail, "多时刻") {
		t.Fatalf("detail = %q", r.ProbeDetail)
	}
	t1Note := t1.Local().Format("15:04") + "=异常/跳过"
	t2Note := t2.Local().Format("15:04") + "=正常/正常"
	if !strings.Contains(r.ProbeDetail, t1Note) || !strings.Contains(r.ProbeDetail, t2Note) {
		t.Fatalf("detail = %q, want %q and %q", r.ProbeDetail, t1Note, t2Note)
	}
	if strings.Index(r.ProbeDetail, t1Note) > strings.Index(r.ProbeDetail, t2Note) {
		t.Fatalf("trail not chronological: %q", r.ProbeDetail)
	}
	if got := atomic.LoadInt32(&fr.retryCalls); got != 0 {
		t.Fatalf("fixed mode must not retry, retryCalls=%d", got)
	}
	if r.ProbeInstant != t2.Local().Format("01-02 15:04:05") {
		t.Fatalf("ProbeInstant = %q", r.ProbeInstant)
	}
}

func TestMultiInstantSingleKeepsPlainDetail(t *testing.T) {
	svc, _, _ := newTestService(t)
	svc.AVAt = []time.Time{testClipStart}
	recs := []model.Record{makeRecord("3101", "31", "cam31", boolPtr(true), true, "")}
	if err := svc.Run(recs); err != nil {
		t.Fatal(err)
	}
	if recs[0].VideoProbe != model.StatusOK {
		t.Fatalf("video = %s", recs[0].VideoProbe)
	}
	if strings.Contains(recs[0].ProbeDetail, "多时刻") {
		t.Fatalf("single instant must not get multi-instant prefix: %q", recs[0].ProbeDetail)
	}
}

// ---------------------------------------------------------------------------
// progress

func TestProgressPhaseAndOverall(t *testing.T) {
	svc, _, _ := newTestService(t)
	var events []model.Progress
	svc.Progress = func(p model.Progress) { events = append(events, p) }
	recs := []model.Record{
		makeRecord("101", "1", "cam1", boolPtr(true), true, ""),
		makeRecord("201", "2", "cam2", boolPtr(true), true, ""),
	}
	if err := svc.Run(recs); err != nil {
		t.Fatal(err)
	}
	if len(events) == 0 {
		t.Fatal("no progress events")
	}
	sawAV := false
	for _, e := range events {
		if e.Phase == "av" {
			sawAV = true
		}
	}
	if !sawAV {
		t.Fatalf("no phase=av progress: %+v", events)
	}
	last := events[len(events)-1]
	if last.Overall != 0.96 || last.Message != "深度抽检完成" {
		t.Fatalf("last progress = %+v", last)
	}
}

// ---------------------------------------------------------------------------
// unit-level helpers

func TestAudioVerdict(t *testing.T) {
	yes, no := true, false
	ok := -20.0
	silent := -90.0
	cases := []struct {
		name    string
		status  string
		expect  *bool
		mean    *float64
		silence float64
		want    string
		note    string
	}{
		{"ok", "ok", &yes, &ok, -80, model.StatusOK, ""},
		{"silence", "ok", &yes, &silent, -80, model.StatusWarn, "静音"},
		{"no stream expected", "no_stream", &yes, nil, -80, model.StatusBad, "无音轨"},
		{"no stream unknown config", "no_stream", nil, nil, -80, model.StatusBad, "无音轨"},
		{"no stream not configured", "no_stream", &no, nil, -80, model.StatusSkip, "未开"},
		{"pull fail", "fail", &yes, nil, -80, model.StatusUnknown, "未确认"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, note := audioVerdict(c.status, c.expect, c.mean, c.silence)
			if got != c.want {
				t.Fatalf("status = %s, want %s", got, c.want)
			}
			if c.note != "" && !strings.Contains(note, c.note) {
				t.Fatalf("note = %q, want contains %q", note, c.note)
			}
		})
	}
}

func TestAVNeedsRetry(t *testing.T) {
	cases := []struct {
		video, audio, detail string
		want                 bool
	}{
		{model.StatusBad, model.StatusSkip, "", true},
		{model.StatusOK, model.StatusUnknown, "", true},
		{model.StatusSkip, model.StatusSkip, "无回放URI", true},
		{model.StatusSkip, model.StatusSkip, "检索失败", true},
		{model.StatusOK, model.StatusOK, "", false},
		{model.StatusOK, model.StatusWarn, "", false},
		{model.StatusSkip, model.StatusSkip, "未配置录像", false},
	}
	for _, c := range cases {
		if got := avNeedsRetry(c.video, c.audio, c.detail); got != c.want {
			t.Errorf("avNeedsRetry(%s,%s,%q) = %v, want %v", c.video, c.audio, c.detail, got, c.want)
		}
	}
}

func TestAVSeverityAndWindowRank(t *testing.T) {
	if avSeverity(model.StatusOK, model.StatusOK) >= avSeverity(model.StatusBad, model.StatusSkip) {
		t.Fatal("bad video should be more severe than ok")
	}
	if avSeverity(model.StatusOK, model.StatusWarn) >= avSeverity(model.StatusOK, model.StatusUnknown) {
		t.Fatal("unknown audio should be more severe than warning")
	}
	// A confirmed failure must beat a missing-evidence window, even though
	// "未知" is numerically less severe than "异常".
	if !rankLess(asRank(model.StatusBad, model.StatusBad), asRank(model.StatusUnknown, model.StatusUnknown)) {
		t.Fatal("definite failure should outrank unknown window")
	}
}

func TestStderrNoMappedStream(t *testing.T) {
	if !stderrNoMappedStream("Stream map '0:a:0' matches no streams.") {
		t.Fatal("matches no streams not detected")
	}
	if !stderrNoMappedStream("Output file does not contain any stream") {
		t.Fatal("output file pattern not detected")
	}
	if stderrNoMappedStream("connection timed out") {
		t.Fatal("false positive")
	}
}

func TestMaskCredentials(t *testing.T) {
	secret := "to" + "p:" + "ps"
	plain := "rtsp://admin:" + secret + "@192.168.1.64/Streaming/tracks/101/"
	got := maskCredentials(plain)
	if strings.Contains(got, secret) || !strings.Contains(got, "***:***@") {
		t.Fatalf("plain = %q", got)
	}
	encodedSecret := strings.ReplaceAll(secret, ":", "%3A")
	encoded := "rtsp://admin:" + encodedSecret + "@192.168.1.64/Streaming"
	got = maskCredentials(encoded)
	if strings.Contains(got, encodedSecret) || !strings.Contains(got, "***:***@") {
		t.Fatalf("encoded = %q", got)
	}
}

func TestProbeRecordUsesRecordURIDirectly(t *testing.T) {
	svc, tools, _ := newTestService(t)
	svc.Rec = nil // the record already carries everything needed
	rec := makeRecord("101", "1", "cam101", boolPtr(true), true, "")
	rec.PlaybackURI = testURI("101", "192.168.1.64")
	s := testClipStart
	e := testClipStart.Add(10 * time.Minute)
	rec.SegStart = &s
	rec.SegEnd = &e
	if err := svc.ProbeRecord(&rec, testClipStart, testClipStart.Add(3*time.Second), "测试窗"); err != nil {
		t.Fatal(err)
	}
	if rec.VideoProbe != model.StatusOK || rec.AudioProbe != model.StatusOK {
		t.Fatalf("status = %s/%s detail=%s", rec.VideoProbe, rec.AudioProbe, rec.ProbeDetail)
	}
	if len(readCalls(t, tools.log)) == 0 {
		t.Fatal("ProbeRecord did not call ffmpeg")
	}
}
