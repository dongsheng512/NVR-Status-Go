// Package avprobe ports nvr_core/av_probe.py: short RTSP audio/video sampling
// with ffmpeg/ffprobe.
//
// The probe deliberately uses `-c copy` and a hard time limit: it only reads a
// few seconds of playback from the NVR, never re-encodes and never downloads a
// whole segment. Audio is pulled separately (`-map 0:a:0 -vn`) because some
// PTZ cameras hang on a full-stream demux.
package avprobe

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"nvrstatus/internal/limit"
	"nvrstatus/internal/model"
	"nvrstatus/internal/recording"
	"nvrstatus/internal/util"
)

// Recording is the narrow slice of the recording service the probe needs.
type Recording interface {
	SearchTrackInRange(trackID string, start, end time.Time) recording.SearchResult
	PickBusyClipTimes(seconds int, at *time.Time) (time.Time, time.Time, string)
	PickRetryClipTimes(seconds int, avoidS, avoidE time.Time) (time.Time, time.Time, string, bool)
}

// Service performs deep audio/video checks on recording tracks.
type Service struct {
	Rec     Recording
	FFmpeg  string
	FFprobe string

	AVSeconds  int
	AVWorkers  int
	AVLimit    int
	AVChannels []int
	// AVAt switches to fixed-instant mode. Each instant is probed separately
	// and the best window is kept (the trail is written into ProbeDetail).
	AVAt []time.Time

	SilenceDB  float64
	AVSave     bool
	AVSaveRoot string

	Username string
	Password string

	// GlobalAV caps ffmpeg processes across all devices (nil = no global cap).
	GlobalAV *limit.Limiter

	// Location is the device wall-clock timezone used to format RTSP
	// starttime/endtime and clip file names. Defaults to the recording
	// service's device timezone, then to time.Local.
	Location *time.Location

	Progress func(model.Progress)
	Log      func(string)
	// Ctx cancels the run and every child process it spawns. nil means
	// "never cancelled". This is the context itself, not a poll predicate:
	// exec.CommandContext reacts the moment it is done.
	Ctx context.Context
	Now func() time.Time

	saveMu  sync.Mutex
	saveDir string

	locMu    sync.Mutex
	locCache *time.Location
}

// ---------------------------------------------------------------------------
// small helpers

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Service) log(msg string) {
	if s.Log != nil {
		s.Log(msg)
	}
}

func (s *Service) progress(msg string, current, total int, phase string, overall float64) {
	if s.Progress != nil {
		s.Progress(model.Progress{
			Message: msg,
			Phase:   phase,
			Current: current,
			Total:   total,
			Overall: overall,
		})
	}
}

// ctx returns the cancellation context, never nil.
func (s *Service) ctx() context.Context {
	if s.Ctx == nil {
		return context.Background()
	}
	return s.Ctx
}

func (s *Service) checkCancel() error {
	return util.CheckCancel(s.ctx())
}

func (s *Service) avSeconds() int {
	v := s.AVSeconds
	if v <= 0 {
		v = 6
	}
	if v < 3 {
		v = 3
	}
	if v > 12 {
		v = 12
	}
	return v
}

// silenceDB returns the configured silence threshold. A zero-value Service
// gets the Python default (-80 dB) instead of a 0 dB threshold, which would
// turn every normal audio track into a warning.
func (s *Service) silenceDB() float64 {
	if s.SilenceDB == 0 {
		return -80
	}
	return s.SilenceDB
}

func (s *Service) workers() int {
	w := s.AVWorkers
	if w < 1 {
		w = 2
	}
	if w > 3 {
		w = 3
	}
	return w
}

// location returns the device wall-clock timezone. Python resolves it from
// /System/status and warms it before starting worker threads; doing that here
// lazily (with a mutex) gives the same one-probe-per-service behaviour.
func (s *Service) location() *time.Location {
	s.locMu.Lock()
	defer s.locMu.Unlock()
	if s.locCache != nil {
		return s.locCache
	}
	loc := s.Location
	if loc == nil {
		if rs, ok := s.Rec.(*recording.Service); ok && rs != nil && rs.F != nil {
			loc = rs.F.DeviceLocation()
		}
	}
	if loc == nil {
		loc = time.Local
	}
	s.locCache = loc
	return loc
}

func statusOrUnknown(v string) string {
	if v == "" {
		return model.StatusUnknown
	}
	return v
}

func lastErrLine(text string) string {
	lines := strings.Split(strings.TrimSpace(text), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if strings.TrimSpace(lines[i]) != "" {
			return strings.TrimSpace(lines[i])
		}
	}
	return ""
}

func truncateRunes(text string, max int) string {
	r := []rune(text)
	if len(r) <= max {
		return text
	}
	return string(r[:max])
}

func fileSize(path string) int64 {
	st, err := os.Stat(path)
	if err != nil || st.IsDir() {
		return 0
	}
	return st.Size()
}

func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}

func maxTime(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

func absDuration(d time.Duration) time.Duration {
	if d < 0 {
		return -d
	}
	return d
}

func numericChannel(raw string) (int, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, false
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return 0, false
	}
	return n, true
}

// ---------------------------------------------------------------------------
// credential masking / RTSP candidate construction

var (
	credPlainRe = regexp.MustCompile(`(://)([^@/\s:]+):([^@/\s]+)@`)
	credEncRe   = regexp.MustCompile(`(?i)(://)([^@/\s%]+)%3A([^@/\s]+)@`)

	startTimeRe = regexp.MustCompile(`(?i)starttime=\d{8}T\d{6}Z`)
	endTimeRe   = regexp.MustCompile(`(?i)endtime=\d{8}T\d{6}Z`)
	sizeParamRe = regexp.MustCompile(`(?i)[&?]size=\d+`)
	doubleAmpRe = regexp.MustCompile(`&&+`)

	rtspRangeRe = regexp.MustCompile(`(?i)starttime=(\d{8}T\d{6}Z).*endtime=(\d{8}T\d{6}Z)`)
)

// maskCredentials removes rtsp user:pass from error text before it can reach a
// report or history file. Both plain and URL-encoded password forms are masked.
func maskCredentials(text string) string {
	if text == "" {
		return text
	}
	text = credPlainRe.ReplaceAllString(text, `${1}***:***@`)
	text = credEncRe.ReplaceAllString(text, `${1}***:***@`)
	return text
}

func escapeUserInfo(value string) string {
	return strings.ReplaceAll(url.QueryEscape(value), "+", "%20")
}

// injectRTSPAuth injects user:pass into an rtsp:// URI, replacing any existing
// userinfo.
func (s *Service) injectRTSPAuth(uri string) string {
	if !strings.HasPrefix(uri, "rtsp://") {
		return uri
	}
	rest := uri[len("rtsp://"):]
	slash := strings.Index(rest, "/")
	authority, path := rest, ""
	if slash >= 0 {
		authority, path = rest[:slash], rest[slash:]
	}
	if at := strings.LastIndex(authority, "@"); at >= 0 {
		authority = authority[at+1:]
	}
	return "rtsp://" + escapeUserInfo(s.Username) + ":" + escapeUserInfo(s.Password) + "@" + authority + path
}

// swapRTSPPort rewrites the explicit port of an rtsp:// URI. It returns false
// when the URI has no explicit port or already uses the requested one (the
// default RTSP port is 554, so an explicit variant is unnecessary).
func swapRTSPPort(uri string, port int) (string, bool) {
	if !strings.HasPrefix(uri, "rtsp://") {
		return "", false
	}
	rest := uri[len("rtsp://"):]
	slash := strings.Index(rest, "/")
	authority, path := rest, ""
	if slash >= 0 {
		authority, path = rest[:slash], rest[slash:]
	}
	hostport := authority
	if at := strings.LastIndex(authority, "@"); at >= 0 {
		hostport = authority[at+1:]
	}
	var host, cur string
	if strings.HasPrefix(hostport, "[") {
		end := strings.Index(hostport, "]")
		if end < 0 {
			return "", false
		}
		host = hostport[:end+1]
		tail := hostport[end+1:]
		if strings.HasPrefix(tail, ":") {
			cur = tail[1:]
		}
	} else if idx := strings.LastIndex(hostport, ":"); idx >= 0 {
		host, cur = hostport[:idx], hostport[idx+1:]
	} else {
		host = hostport
	}
	if cur == "" {
		return "", false
	}
	current, err := strconv.Atoi(cur)
	if err != nil || current == port {
		return "", false
	}
	userinfo := authority[:len(authority)-len(hostport)]
	return "rtsp://" + userinfo + host + ":" + strconv.Itoa(port) + path, true
}

// fmtRTSPTime formats a time as the device local wall clock, still using the Z
// suffix (common Chinese NVR firmware behaviour).
func (s *Service) fmtRTSPTime(dt time.Time) string {
	return dt.In(s.location()).Format("20060102T150405") + "Z"
}

func (s *Service) fmtRTSPTimeMode(dt time.Time, mode string) string {
	if mode == "utc" {
		return dt.UTC().Format("20060102T150405") + "Z"
	}
	return s.fmtRTSPTime(dt)
}

func (s *Service) parseRTSPURITime(text, mode string) (time.Time, error) {
	raw := strings.TrimSuffix(strings.ToUpper(strings.TrimSpace(text)), "Z")
	naive, err := time.Parse("20060102T150405", raw)
	if err != nil {
		return time.Time{}, err
	}
	if mode == "utc" {
		return naive.UTC(), nil
	}
	loc := s.location()
	return time.Date(naive.Year(), naive.Month(), naive.Day(),
		naive.Hour(), naive.Minute(), naive.Second(), 0, loc).UTC(), nil
}

// detectRTSPTimeMode compares the URI's embedded start time with the CMSearch
// segment start. Without a usable reference it defaults to local wall clock
// (matching the write side and the common domestic firmware).
func (s *Service) detectRTSPTimeMode(uriStartRaw string, segStart *time.Time) string {
	if uriStartRaw == "" || segStart == nil {
		return "local"
	}
	asUTC, errUTC := s.parseRTSPURITime(uriStartRaw, "utc")
	asLocal, errLocal := s.parseRTSPURITime(uriStartRaw, "local")
	if errUTC != nil || errLocal != nil {
		return "local"
	}
	seg := segStart.UTC()
	dUTC := absDuration(asUTC.Sub(seg))
	dLocal := absDuration(asLocal.Sub(seg))
	if dUTC+2*time.Second < dLocal {
		return "utc"
	}
	return "local"
}

// computeClipWindow cuts a short probe window inside [uriS, uriE]. Its end is
// never later than "now - 10 minutes" to avoid probing a segment still being
// written (which tends to make RTSP playback time out).
func (s *Service) computeClipWindow(uriS, uriE time.Time, seconds int, clipStart, clipEnd *time.Time) (time.Time, time.Time) {
	now := s.now().UTC()
	cutoff := util.LatestAVSampleInstant(now)
	sec := max(1, seconds)

	uriS, uriE = uriS.UTC(), uriE.UTC()
	hardE := minTime(uriE, cutoff)

	var clipS, clipE time.Time
	if clipStart != nil && clipEnd != nil {
		cs, ce := clipStart.UTC(), clipEnd.UTC()
		clipS = maxTime(uriS, cs)
		clipE = minTime(hardE, ce)
		if !clipE.After(clipS) {
			if ce.After(cutoff) || !cs.Before(cutoff) {
				clipE = hardE
				clipS = maxTime(uriS, clipE.Add(-time.Duration(sec)*time.Second))
			} else {
				clipS = uriS
				clipE = minTime(hardE, uriS.Add(time.Duration(sec)*time.Second))
			}
		}
		if clipE.Sub(clipS) < time.Duration(max(1, sec/2))*time.Second {
			clipE = minTime(hardE, clipS.Add(time.Duration(sec)*time.Second))
		}
	} else {
		clipE = minTime(hardE, uriE.Add(-2*time.Second))
		if !clipE.After(uriS) {
			clipE = hardE
		}
		clipS = clipE.Add(-time.Duration(sec) * time.Second)
		if clipS.Before(uriS) {
			clipS = uriS
		}
		if !clipE.After(clipS) {
			clipE = minTime(hardE, clipS.Add(time.Duration(sec)*time.Second))
		}
	}
	if !clipE.After(clipS) {
		clipS = uriS
		clipE = minTime(hardE, uriS.Add(time.Duration(sec)*time.Second))
	}
	if !clipE.After(clipS) {
		clipE = clipS.Add(time.Duration(sec) * time.Second)
	}
	if clipE.After(cutoff) {
		clipE = cutoff
		clipS = minTime(clipS, clipE.Add(-time.Duration(sec)*time.Second))
		if clipS.Before(uriS) {
			clipS = uriS
		}
	}
	return clipS, clipE
}

// rewriteRTSPTimes replaces starttime/endtime, strips the size parameter and
// keeps every other query parameter untouched.
func (s *Service) rewriteRTSPTimes(playbackURI string, clipS, clipE time.Time, mode string) string {
	short := startTimeRe.ReplaceAllStringFunc(playbackURI, func(string) string {
		return "starttime=" + s.fmtRTSPTimeMode(clipS, mode)
	})
	short = endTimeRe.ReplaceAllStringFunc(short, func(string) string {
		return "endtime=" + s.fmtRTSPTimeMode(clipE, mode)
	})
	short = sizeParamRe.ReplaceAllString(short, "")
	short = strings.ReplaceAll(short, "?&", "?")
	short = doubleAmpRe.ReplaceAllString(short, "&")
	short = strings.TrimRight(short, "?&")
	return short
}

type candidate struct {
	Label string
	URL   string
}

// candidateOrder sorts candidates: original URI first, then rewritten windows,
// with every `@554` fallback last. `@554` is only a fallback for a broken
// original port; letting it jump the queue would add another long timeout to
// every normal probe.
func candidateOrder(label string) (int, int, string) {
	portRank := 0
	if strings.HasSuffix(label, "@554") {
		portRank = 1
	}
	originRank := 1
	if strings.HasPrefix(label, "original") {
		originRank = 0
	}
	return portRank, originRank, label
}

func sortCandidates(in []candidate) []candidate {
	out := append([]candidate(nil), in...)
	sort.SliceStable(out, func(i, j int) bool {
		pi, oi, li := candidateOrder(out[i].Label)
		pj, oj, lj := candidateOrder(out[j].Label)
		if pi != pj {
			return pi < pj
		}
		if oi != oj {
			return oi < oj
		}
		return li < lj
	})
	return out
}

// buildShortRTSPCandidates mirrors Python _build_short_rtsp_candidates:
//  1. short window + detected time mode (local/utc)
//  2. short window + the other mode (firmware differences / 400)
//  3. original URI with only authentication injected (most conservative)
//
// Every candidate gets an `@554` port variant when the URI carries an explicit
// non-554 port.
func (s *Service) buildShortRTSPCandidates(playbackURI string, segStart, segEnd *time.Time, seconds int, clipStart, clipEnd *time.Time) []candidate {
	if !strings.HasPrefix(playbackURI, "rtsp://") {
		return nil
	}
	now := s.now().UTC()
	m := rtspRangeRe.FindStringSubmatch(playbackURI)
	mode := "local"
	uriS, uriE := now, now
	haveS, haveE := false, false
	if m != nil {
		mode = s.detectRTSPTimeMode(m[1], segStart)
		if t, err := s.parseRTSPURITime(m[1], mode); err == nil {
			uriS, haveS = t, true
		}
		if t, err := s.parseRTSPURITime(m[2], mode); err == nil {
			uriE, haveE = t, true
		}
	}
	if segStart != nil {
		uriS, haveS = segStart.UTC(), true
	}
	if segEnd != nil {
		uriE, haveE = segEnd.UTC(), true
	}
	if !haveE {
		uriE = now
	}
	if !haveS {
		uriS = uriE.Add(-5 * time.Minute)
	}
	if !uriE.After(uriS) {
		uriE = uriS.Add(time.Duration(max(1, seconds)) * time.Second)
	}
	clipS, clipE := s.computeClipWindow(uriS, uriE, seconds, clipStart, clipEnd)

	var out []candidate
	seen := map[string]bool{}
	add := func(label, u string) {
		if u == "" || seen[u] {
			return
		}
		seen[u] = true
		out = append(out, candidate{Label: label, URL: u})
	}
	addWithPort := func(label, u string) {
		add(label, u)
		if alt, ok := swapRTSPPort(u, 554); ok {
			add(label+"@554", alt)
		}
	}

	if m != nil {
		alt := "utc"
		if mode == "utc" {
			alt = "local"
		}
		for _, md := range []string{mode, alt} {
			rewritten := s.rewriteRTSPTimes(playbackURI, clipS, clipE, md)
			addWithPort("short/"+md, s.injectRTSPAuth(rewritten))
		}
	}
	addWithPort("original", s.injectRTSPAuth(playbackURI))
	return out
}

// buildShortRTSP returns the first (preferred) short RTSP URL, or "".
func (s *Service) buildShortRTSP(playbackURI string, segStart, segEnd *time.Time, seconds int, clipStart, clipEnd *time.Time) string {
	cands := s.buildShortRTSPCandidates(playbackURI, segStart, segEnd, seconds, clipStart, clipEnd)
	if len(cands) == 0 {
		return ""
	}
	return cands[0].URL
}

// ---------------------------------------------------------------------------
// clip saving

// AVSaveDir returns the directory created for this run ("" when AVSave is off
// or nothing has been probed yet).
func (s *Service) AVSaveDir() string {
	s.saveMu.Lock()
	defer s.saveMu.Unlock()
	return s.saveDir
}

func (s *Service) prepareAVSaveDir() string {
	if !s.AVSave {
		return ""
	}
	s.saveMu.Lock()
	defer s.saveMu.Unlock()
	if s.saveDir != "" {
		if st, err := os.Stat(s.saveDir); err == nil && st.IsDir() {
			return s.saveDir
		}
	}
	root := s.AVSaveRoot
	if root == "" {
		root = "av_samples"
	}
	base := filepath.Join(root, s.now().Local().Format("20060102_150405"))
	path := base
	for n := 1; ; n++ {
		if _, err := os.Stat(path); os.IsNotExist(err) {
			break
		}
		path = fmt.Sprintf("%s_%d", base, n)
	}
	if err := os.MkdirAll(path, 0o755); err != nil {
		s.log("创建抽检保存目录失败: " + err.Error())
		return ""
	}
	s.saveDir = path
	return path
}

// saveClipFile copies a successfully pulled temporary clip into the run's save
// directory. It returns "" when saving is disabled or the copy fails.
func (s *Service) saveClipFile(tmpPath, trackID, channel, name string, clipStart *time.Time) string {
	saveDir := s.prepareAVSaveDir()
	if saveDir == "" || tmpPath == "" {
		return ""
	}
	if st, err := os.Stat(tmpPath); err != nil || st.IsDir() {
		return ""
	}
	chSource := channel
	if chSource == "" {
		chSource = trackID
	}
	nameSource := name
	if nameSource == "" {
		nameSource = "cam"
	}
	ch := util.SafeFilename(chSource, 16)
	nm := util.SafeFilename(nameSource, 40)
	tid := util.SafeFilename(trackID, 16)
	var ttag string
	if clipStart != nil {
		ttag = clipStart.In(s.location()).Format("150405")
	} else {
		ttag = s.now().In(s.location()).Format("150405")
	}
	fname := fmt.Sprintf("ch%s_%s_track%s_%s.mkv", ch, nm, tid, ttag)
	dest := filepath.Join(saveDir, fname)
	if _, err := os.Stat(dest); err == nil {
		ext := filepath.Ext(fname)
		stem := strings.TrimSuffix(fname, ext)
		for i := 1; ; i++ {
			dest = filepath.Join(saveDir, fmt.Sprintf("%s_%d%s", stem, i, ext))
			if _, err := os.Stat(dest); os.IsNotExist(err) {
				break
			}
		}
	}
	src, err := os.Open(tmpPath)
	if err != nil {
		return ""
	}
	defer src.Close()
	dst, err := os.Create(dest)
	if err != nil {
		return ""
	}
	if _, err := io.Copy(dst, src); err != nil {
		dst.Close()
		_ = os.Remove(dest)
		return ""
	}
	if err := dst.Close(); err != nil {
		_ = os.Remove(dest)
		return ""
	}
	return dest
}

// ---------------------------------------------------------------------------
// process execution

var errCommandTimeout = errors.New("command timeout")

type commandResult struct {
	Stdout   string
	Stderr   string
	ExitCode int
}

// runCommand executes a child process and kills it as soon as the run context
// is done. Non-zero exit codes are data, not errors (ffmpeg uses them
// routinely); launch failures and timeouts are returned as errors.
func (s *Service) runCommand(args []string, timeout time.Duration) (*commandResult, error) {
	if len(args) == 0 {
		return nil, errors.New("empty command")
	}
	if err := s.checkCancel(); err != nil {
		return nil, err
	}

	// Derive from the run context so cancellation reaches exec.CommandContext
	// directly. The Python original had to poll subprocess.poll() every 0.25s
	// because Popen.wait() cannot be interrupted; Go does not.
	ctx := s.ctx()
	if timeout > 0 {
		var cancelTimeout context.CancelFunc
		ctx, cancelTimeout = context.WithTimeout(ctx, timeout)
		defer cancelTimeout()
	}

	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	cmd.WaitDelay = 1 * time.Second

	runErr := cmd.Run()

	res := &commandResult{Stdout: stdout.String(), Stderr: stderr.String(), ExitCode: -1}
	if cmd.ProcessState != nil {
		res.ExitCode = cmd.ProcessState.ExitCode()
	}
	if ctx.Err() != nil {
		// An aborted run must not be reported as "this clip timed out".
		if s.checkCancel() != nil {
			return res, util.ErrCancelled
		}
		return res, errCommandTimeout
	}
	if runErr != nil {
		var exitErr *exec.ExitError
		if errors.As(runErr, &exitErr) {
			return res, nil
		}
		return res, runErr
	}
	return res, nil
}

// ---------------------------------------------------------------------------
// RTSP pulling

type pullOptions struct {
	MinOK           int64
	PartialOK       int64
	TimeoutOriginal time.Duration
	TimeoutOther    time.Duration
	SockOriginal    int64
	SockOther       int64
	StopOnNoStream  bool
}

type pullResult struct {
	Status  string // ok / no_stream / fail
	Size    int64
	LastErr string
	Used    *candidate
}

var noMappedStreamPatterns = []string{
	"matches no streams",
	"does not contain any stream",
	"output file does not contain any stream",
}

func stderrNoMappedStream(text string) bool {
	lower := strings.ToLower(text)
	for _, pattern := range noMappedStreamPatterns {
		if strings.Contains(lower, pattern) {
			return true
		}
	}
	return false
}

// pullRTSPMap pulls one stream map from the candidate list into tmpPath.
func (s *Service) pullRTSPMap(ffmpeg string, candidates []candidate, tmpPath, streamMap string, seconds int, extraArgs []string, opt pullOptions) (*pullResult, error) {
	if opt.TimeoutOriginal <= 0 {
		opt.TimeoutOriginal = time.Duration(s.avSeconds()+25) * time.Second
	}
	if opt.TimeoutOther <= 0 {
		opt.TimeoutOther = time.Duration(s.avSeconds()+30) * time.Second
	}
	if opt.SockOriginal <= 0 {
		opt.SockOriginal = 12_000_000
	}
	if opt.SockOther <= 0 {
		opt.SockOther = 10_000_000
	}
	lastErr := ""
	var size int64
	sawNoStream := false
	for i := range candidates {
		if err := s.checkCancel(); err != nil {
			return nil, err
		}
		cand := &candidates[i]
		if _, err := os.Stat(tmpPath); err == nil {
			_ = os.Truncate(tmpPath, 0)
		}
		timeout := opt.TimeoutOther
		sock := opt.SockOther
		if strings.HasPrefix(cand.Label, "original") {
			timeout = opt.TimeoutOriginal
			sock = opt.SockOriginal
		}
		args := []string{
			ffmpeg, "-y",
			"-hide_banner", "-loglevel", "error",
			"-rtsp_transport", "tcp",
			"-timeout", strconv.FormatInt(sock, 10),
			"-i", cand.URL,
			"-t", strconv.Itoa(seconds),
		}
		args = append(args, extraArgs...)
		args = append(args, "-map", streamMap, "-c", "copy", "-f", "matroska", tmpPath)
		res, err := s.runCommand(args, timeout)
		if err != nil {
			if errors.Is(err, util.ErrCancelled) {
				return nil, err
			}
			if errors.Is(err, errCommandTimeout) {
				size = fileSize(tmpPath)
				if size >= opt.PartialOK {
					return &pullResult{Status: "ok", Size: size, Used: cand}, nil
				}
				lastErr = fmt.Sprintf("拉流超时(%s,%ss)", cand.Label, strconv.FormatFloat(timeout.Seconds(), 'f', -1, 64))
				continue
			}
			// Launch failure (missing binary etc.) is a configuration error,
			// not a media verdict: surface it to the caller as an exception,
			// matching the Python implementation's generic handler.
			return nil, err
		}
		size = fileSize(tmpPath)
		errText := res.Stderr
		if stderrNoMappedStream(errText) {
			sawNoStream = true
			lastErr = lastErrLine(errText)
			if lastErr == "" {
				lastErr = "no mapped stream"
			}
			if opt.StopOnNoStream {
				return &pullResult{Status: "no_stream", Size: size, LastErr: lastErr}, nil
			}
			continue
		}
		if size >= opt.MinOK && (res.ExitCode == 0 || size >= opt.PartialOK) {
			return &pullResult{Status: "ok", Size: size, Used: cand}, nil
		}
		if line := lastErrLine(errText); line != "" {
			lastErr = line
		} else {
			lastErr = fmt.Sprintf("rc=%d", res.ExitCode)
		}
	}
	if sawNoStream && size < opt.MinOK {
		return &pullResult{Status: "no_stream", Size: size, LastErr: lastErr}, nil
	}
	return &pullResult{Status: "fail", Size: size, LastErr: lastErr}, nil
}

// ---------------------------------------------------------------------------
// probing

type probeResult struct {
	VideoProbe   string
	AudioProbe   string
	Detail       string
	VideoCodec   string
	AudioCodec   string
	Resolution   string
	MeanVolumeDB *float64
	Window       string
	Instant      string
	SavePath     string
}

type audioProbeResult struct {
	Probe string
	Codec string
	Mean  *float64
	Note  string
}

// audioVerdict maps an audio pull status to (音频抽检, 备注).
//
// Two rules worth keeping explicit:
//   - "ok" with no level reading is 未知, not 正常. volumedetect prints no
//     mean_volume line at all for a track with zero decodable samples, and a
//     failed decode has no reading either; neither is evidence of working audio.
//   - "no_stream" with an unknown config is 未知, not 异常. SaveAudio is not
//     returned by every firmware, and a microphone-less camera would otherwise
//     be escalated to 严重 on missing config data alone.
//
// See docs/python-port-analysis-2026-11-04.md §7 for the measurements behind these.
func audioVerdict(status string, expectAudio *bool, meanDB *float64, silenceDB float64) (string, string) {
	switch status {
	case "ok":
		if meanDB == nil {
			return model.StatusUnknown, "音频未确认(电平未测到)"
		}
		if *meanDB <= silenceDB {
			return model.StatusWarn, fmt.Sprintf("疑似静音(mean %.1fdB)", *meanDB)
		}
		return model.StatusOK, ""
	case "silent":
		// -inf dB: the track exists but every sample is zero. This is a real
		// silence verdict, not a failed measurement.
		return model.StatusWarn, "疑似静音(无有效采样)"
	case "no_stream":
		if expectAudio == nil {
			return model.StatusUnknown, "音频未确认(配置未知且无音轨)"
		}
		if !*expectAudio {
			return model.StatusSkip, "配置未开音频"
		}
		return model.StatusBad, "无音轨"
	default:
		return model.StatusUnknown, "音频未确认(拉流超时/失败)"
	}
}

var audioSev = map[string]int{
	model.StatusOK:      0,
	model.StatusWarn:    1,
	model.StatusSkip:    2,
	model.StatusUnknown: 3,
	model.StatusBad:     4,
}

func severityOf(status string) int {
	if v, ok := audioSev[statusOrUnknown(status)]; ok {
		return v
	}
	return 3
}

func avSeverity(video, audio string) int {
	return max(severityOf(video), severityOf(audio))
}

func avNeedsRetry(video, audio, detail string) bool {
	if video == model.StatusBad || video == model.StatusUnknown ||
		audio == model.StatusBad || audio == model.StatusUnknown {
		return true
	}
	if video == model.StatusSkip {
		for _, key := range []string{"无回放", "无录像", "检索失败"} {
			if strings.Contains(detail, key) {
				return true
			}
		}
	}
	return false
}

func atWindowRank(video, audio string) (int, int) {
	definite := 1
	if video == model.StatusOK || video == model.StatusWarn || video == model.StatusBad ||
		audio == model.StatusOK || audio == model.StatusWarn || audio == model.StatusBad {
		definite = 0
	}
	return definite, avSeverity(video, audio)
}

func rankLess(a, b [2]int) bool {
	if a[0] != b[0] {
		return a[0] < b[0]
	}
	return a[1] < b[1]
}

func asRank(video, audio string) [2]int {
	d, s := atWindowRank(video, audio)
	return [2]int{d, s}
}

// probeAudioTrack pulls the audio track separately and runs volumedetect.
func (s *Service) probeAudioTrack(ffmpeg, ffprobe string, candidates []candidate, expectAudio *bool) (*audioProbeResult, error) {
	audioSeconds := max(3, min(s.avSeconds(), 4))
	tmp, err := os.CreateTemp("", "nvr_a_*.mkv")
	if err != nil {
		return &audioProbeResult{Probe: model.StatusUnknown, Note: "音频抽检异常: " + truncateRunes(maskCredentials(err.Error()), 80)}, nil
	}
	tmpPath := tmp.Name()
	_ = tmp.Close()
	defer func() { _ = os.Remove(tmpPath) }()

	pull, err := s.pullRTSPMap(ffmpeg, candidates, tmpPath, "0:a:0", audioSeconds,
		[]string{"-vn"},
		pullOptions{
			MinOK:           512,
			PartialOK:       1024,
			TimeoutOriginal: time.Duration(audioSeconds+10) * time.Second,
			TimeoutOther:    time.Duration(audioSeconds+8) * time.Second,
			SockOriginal:    8_000_000,
			SockOther:       6_000_000,
			StopOnNoStream:  true,
		})
	if err != nil {
		if errors.Is(err, util.ErrCancelled) {
			return nil, err
		}
		return &audioProbeResult{Probe: model.StatusUnknown,
			Note: "音频抽检异常: " + truncateRunes(maskCredentials(err.Error()), 80)}, nil
	}
	status := pull.Status
	lastErr := pull.LastErr
	codec := ""
	var meanDB *float64

	if status == "ok" {
		probeCmd := []string{ffprobe, "-v", "error", "-show_streams", "-of", "json", tmpPath}
		p2, perr := s.runCommand(probeCmd, 20*time.Second)
		if perr != nil {
			if errors.Is(perr, util.ErrCancelled) {
				return nil, perr
			}
			return &audioProbeResult{Probe: model.StatusUnknown,
				Note: "音频抽检异常: " + truncateRunes(maskCredentials(perr.Error()), 80)}, nil
		}
		if p2.ExitCode != 0 {
			status = "fail"
			if lastErr == "" {
				lastErr = "ffprobe解析音频失败"
			}
		} else {
			var data struct {
				Streams []struct {
					CodecType string `json:"codec_type"`
					CodecName string `json:"codec_name"`
				} `json:"streams"`
			}
			if jerr := json.Unmarshal([]byte(p2.Stdout), &data); jerr != nil {
				return &audioProbeResult{Probe: model.StatusUnknown,
					Note: "音频抽检异常: " + truncateRunes(maskCredentials(jerr.Error()), 80)}, nil
			}
			found := false
			for _, st := range data.Streams {
				if st.CodecType == "audio" {
					codec = st.CodecName
					found = true
					break
				}
			}
			if !found {
				status = "no_stream"
			} else {
				volCmd := []string{
					ffmpeg, "-hide_banner", "-nostats",
					"-i", tmpPath,
					"-t", strconv.Itoa(audioSeconds),
					"-af", "volumedetect",
					"-f", "null", "-",
				}
				vp, verr := s.runCommand(volCmd, 30*time.Second)
				if verr != nil {
					if errors.Is(verr, util.ErrCancelled) {
						return nil, verr
					}
					return &audioProbeResult{Probe: model.StatusUnknown,
						Note: "音频抽检异常: " + truncateRunes(maskCredentials(verr.Error()), 80)}, nil
				}
				// runCommand reports a non-zero exit through ExitCode, not error.
				// Without this check a failed decode would fall through to
				// "ok with no reading" and be reported as working audio.
				if vp.ExitCode != 0 {
					status = "fail"
					if lastErr == "" {
						lastErr = fmt.Sprintf("volumedetect 退出码 %d", vp.ExitCode)
					}
				} else if m := meanVolumeRe.FindStringSubmatch(vp.Stderr); m != nil {
					if strings.EqualFold(m[1], "-inf") {
						status = "silent"
					} else if v, err := strconv.ParseFloat(m[1], 64); err == nil {
						meanDB = &v
					}
				}
			}
		}
	}
	verdict, note := audioVerdict(status, expectAudio, meanDB, s.silenceDB())
	if verdict == model.StatusUnknown && lastErr != "" {
		note = note + ": " + truncateRunes(maskCredentials(lastErr), 80)
	}
	return &audioProbeResult{Probe: verdict, Codec: codec, Mean: meanDB, Note: note}, nil
}

var (
	// -inf dB is what volumedetect prints when every sample is zero; the old
	// numeric-only pattern silently missed it.
	meanVolumeRe = regexp.MustCompile(`(?i)mean_volume:\s*(-?(?:[0-9]+(?:\.[0-9]+)?|inf))\s*dB`)
	probePointRe = regexp.MustCompile(`抽检点\s+([0-9 :\-]+)`)
)

// probeTrackAV is the core single-track probe. It returns a result even for
// probe failures; only cancellation is returned as an error.
func (s *Service) probeTrackAV(trackID, playbackURI string, segStart, segEnd *time.Time, expectAudio *bool, clipStart, clipEnd *time.Time, sampleLabel, channel, name string, saveClipStart *time.Time) (*probeResult, error) {
	res := &probeResult{
		VideoProbe: model.StatusUnknown,
		AudioProbe: model.StatusUnknown,
		Window:     sampleLabel,
	}
	ffmpeg, ffprobe := s.FFmpeg, s.FFprobe
	if ffmpeg == "" || ffprobe == "" {
		res.Detail = "本机未安装 ffmpeg/ffprobe"
		return res, nil
	}
	if playbackURI == "" {
		res.VideoProbe = model.StatusSkip
		res.AudioProbe = model.StatusSkip
		res.Detail = "无回放URI"
		return res, nil
	}
	cands := s.buildShortRTSPCandidates(playbackURI, segStart, segEnd, s.avSeconds(), clipStart, clipEnd)
	if len(cands) == 0 {
		res.Detail = "无法构造短时RTSP地址"
		return res, nil
	}
	ordered := sortCandidates(cands)

	tmp, err := os.CreateTemp("", "nvr_av_*.mkv")
	if err != nil {
		res.Detail = "抽检异常: " + maskCredentials(err.Error())
		return res, nil
	}
	tmpPath := tmp.Name()
	_ = tmp.Close()
	defer func() { _ = os.Remove(tmpPath) }()

	avSec := s.avSeconds()
	pull, err := s.pullRTSPMap(ffmpeg, ordered, tmpPath, "0:v:0", avSec, nil, pullOptions{
		MinOK:           8 * 1024,
		PartialOK:       24 * 1024,
		TimeoutOriginal: time.Duration(avSec+25) * time.Second,
		TimeoutOther:    time.Duration(avSec+30) * time.Second,
		SockOriginal:    12_000_000,
		SockOther:       10_000_000,
	})
	if err != nil {
		if errors.Is(err, util.ErrCancelled) {
			return nil, err
		}
		res.Detail = "抽检异常: " + maskCredentials(err.Error())
		return res, nil
	}
	size := pull.Size
	if pull.Status != "ok" {
		res.VideoProbe = model.StatusBad
		res.AudioProbe = model.StatusSkip
		lastErr := pull.LastErr
		hint := ""
		low := strings.ToLower(lastErr)
		switch {
		case strings.Contains(lastErr, "超时") || strings.Contains(low, "timeout"):
			hint = "；回放视频轨超时(已试原URI/local/utc)。请检查该通道回放服务"
		case strings.Contains(low, "400") || strings.Contains(low, "bad request"):
			hint = "；多为回放时间窗无效(时区/段外)。已尝试 local/utc 改写与原URI回退"
		case strings.Contains(low, "401") || strings.Contains(low, "unauthor"):
			hint = "；鉴权失败，请核对账号密码"
		case strings.Contains(low, "404"):
			hint = "；回放资源不存在，通道可能无该时段录像"
		}
		res.Detail = fmt.Sprintf("短时拉流失败(%s)%s", truncateRunes(maskCredentials(lastErr), 100), hint)
		return res, nil
	}

	probeCmd := []string{ffprobe, "-v", "error", "-show_streams", "-show_format", "-of", "json", tmpPath}
	p2, perr := s.runCommand(probeCmd, 30*time.Second)
	if perr != nil {
		if errors.Is(perr, util.ErrCancelled) {
			return nil, perr
		}
		if errors.Is(perr, errCommandTimeout) {
			res.VideoProbe = model.StatusBad
			res.AudioProbe = model.StatusUnknown
			res.Detail = "拉流超时"
			return res, nil
		}
		res.Detail = "抽检异常: " + truncateRunes(maskCredentials(perr.Error()), 200)
		return res, nil
	}
	if p2.ExitCode != 0 {
		res.VideoProbe = model.StatusBad
		res.AudioProbe = model.StatusSkip
		res.Detail = "ffprobe解析失败"
		return res, nil
	}
	var data struct {
		Streams []struct {
			CodecType string `json:"codec_type"`
			CodecName string `json:"codec_name"`
			Width     int    `json:"width"`
			Height    int    `json:"height"`
		} `json:"streams"`
	}
	if jerr := json.Unmarshal([]byte(p2.Stdout), &data); jerr != nil {
		res.Detail = "抽检异常: " + maskCredentials(jerr.Error())
		return res, nil
	}
	var videoStream *struct {
		CodecType string `json:"codec_type"`
		CodecName string `json:"codec_name"`
		Width     int    `json:"width"`
		Height    int    `json:"height"`
	}
	for i := range data.Streams {
		if data.Streams[i].CodecType == "video" {
			videoStream = &data.Streams[i]
			break
		}
	}
	if videoStream == nil {
		res.VideoProbe = model.StatusBad
		res.Detail = "无视频轨"
	} else {
		w, h := videoStream.Width, videoStream.Height
		res.VideoCodec = videoStream.CodecName
		if w > 0 && h > 0 {
			res.Resolution = fmt.Sprintf("%dx%d", w, h)
		}
		if w >= 160 && h >= 120 {
			res.VideoProbe = model.StatusOK
		} else {
			res.VideoProbe = model.StatusBad
			resolution := res.Resolution
			if resolution == "" {
				resolution = "None"
			}
			res.Detail = fmt.Sprintf("视频分辨率异常(%s)", resolution)
		}
	}

	if res.VideoProbe != model.StatusOK {
		res.AudioProbe = model.StatusSkip
		return res, nil
	}

	audioCands := make([]candidate, 0, len(ordered))
	if pull.Used != nil {
		audioCands = append(audioCands, *pull.Used)
	}
	for _, item := range ordered {
		if pull.Used == nil || item.URL != pull.Used.URL {
			audioCands = append(audioCands, item)
		}
	}
	audioRes, aerr := s.probeAudioTrack(ffmpeg, ffprobe, audioCands, expectAudio)
	if aerr != nil {
		return nil, aerr
	}
	res.AudioProbe = statusOrUnknown(audioRes.Probe)
	res.AudioCodec = audioRes.Codec
	res.MeanVolumeDB = audioRes.Mean
	audioNote := audioRes.Note

	if res.Detail == "" {
		parts := []string{}
		if res.Resolution != "" {
			parts = append(parts, res.Resolution)
		}
		if res.VideoCodec != "" {
			parts = append(parts, res.VideoCodec)
		}
		if res.AudioCodec != "" {
			parts = append(parts, res.AudioCodec)
		}
		if res.MeanVolumeDB != nil {
			parts = append(parts, fmt.Sprintf("%.1fdB", *res.MeanVolumeDB))
		}
		prefix := "短时抽检OK"
		if sampleLabel != "" {
			if m := probePointRe.FindStringSubmatch(sampleLabel); m != nil {
				prefix = "短时抽检OK@" + strings.TrimSpace(m[1])
			}
		}
		res.Detail = strings.TrimSpace(prefix + " " + strings.Join(parts, " "))
	}
	if audioNote != "" {
		if res.Detail != "" {
			res.Detail = res.Detail + "; " + audioNote
		} else {
			res.Detail = audioNote
		}
	}

	if s.AVSave && size >= 1024 {
		start := clipStart
		if saveClipStart != nil {
			start = saveClipStart
		}
		if saved := s.saveClipFile(tmpPath, trackID, channel, name, start); saved != "" {
			res.SavePath = saved
		} else {
			extra := "保存片段失败"
			if res.Detail != "" {
				res.Detail = res.Detail + "; " + extra
			} else {
				res.Detail = extra
			}
		}
	}
	return res, nil
}

// probeChannelAt searches the fixed window and probes the found playback URI.
func (s *Service) probeChannelAt(rec *model.Record, clipS, clipE time.Time, sampleLabel string) (*probeResult, error) {
	if err := s.checkCancel(); err != nil {
		return nil, err
	}
	found := s.Rec.SearchTrackInRange(rec.TrackID, clipS, clipE)
	uri := found.PlaybackURI
	if uri == "" {
		uri = rec.PlaybackURI
	}
	segStart, segEnd := rec.SegStart, rec.SegEnd
	if found.OK {
		segStart, segEnd = found.SegStart, found.SegEnd
	}
	var useClipS, useClipE *time.Time
	if found.OK {
		useClipS, useClipE = &clipS, &clipE
	}
	label := sampleLabel
	if !found.OK {
		label = sampleLabel + "; 繁忙时段未命中,回退近期片段"
	}
	if uri == "" {
		detail := found.Detail
		if detail == "" {
			detail = "无回放URI"
		}
		return &probeResult{
			VideoProbe: model.StatusSkip,
			AudioProbe: model.StatusSkip,
			Detail:     detail,
			Window:     label,
		}, nil
	}
	saveStart := clipS
	if useClipS != nil {
		saveStart = *useClipS
	}
	return s.probeTrackAV(rec.TrackID, uri, segStart, segEnd, rec.HasAudio,
		useClipS, useClipE, label, rec.Channel, rec.Name, &saveStart)
}

// ---------------------------------------------------------------------------
// result application

func applyAVResult(rec *model.Record, res *probeResult, sampleLabel string) {
	if res == nil {
		res = &probeResult{}
	}
	rec.VideoProbe = statusOrUnknown(res.VideoProbe)
	rec.AudioProbe = statusOrUnknown(res.AudioProbe)
	rec.ProbeDetail = res.Detail
	if res.Window != "" {
		rec.ProbeWindow = res.Window
	} else {
		rec.ProbeWindow = sampleLabel
	}
	rec.VideoCodec = res.VideoCodec
	rec.AudioCodec = res.AudioCodec
	rec.Resolution = res.Resolution
	rec.MeanVolumeDB = res.MeanVolumeDB
	rec.SavePath = res.SavePath
	rec.ProbeInstant = res.Instant
}

func markSkip(rec *model.Record, detail string) {
	rec.VideoProbe = model.StatusSkip
	rec.AudioProbe = model.StatusSkip
	rec.ProbeDetail = detail
}

func (s *Service) applyResults(records []model.Record, indexes []int, results map[int]*probeResult, label string) {
	for _, idx := range indexes {
		applyAVResult(&records[idx], results[idx], label)
	}
}

// ---------------------------------------------------------------------------
// orchestration

type avJobResult struct {
	index int
	res   *probeResult
	err   error
}

// runAVJobs probes the candidate indexes with a small worker pool. Progress is
// emitted from the collector goroutine, keeping the callback single-threaded.
func (s *Service) runAVJobs(records []model.Record, indexes []int, clipS, clipE time.Time, sampleLabel string, progressLo, progressHi float64, progressText string) (map[int]*probeResult, error) {
	results := make(map[int]*probeResult, len(indexes))
	total := len(indexes)
	if total == 0 {
		return results, nil
	}
	jobs := make(chan int, total)
	for _, idx := range indexes {
		jobs <- idx
	}
	close(jobs)

	out := make(chan avJobResult, total)
	var wg sync.WaitGroup
	for w := 0; w < s.workers(); w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for idx := range jobs {
				if err := s.checkCancel(); err != nil {
					out <- avJobResult{index: idx, err: err}
					return
				}
				if s.GlobalAV != nil {
					if err := s.GlobalAV.Acquire(s.checkCancel); err != nil {
						out <- avJobResult{index: idx, err: err}
						return
					}
				}
				res, err := s.probeChannelAt(&records[idx], clipS, clipE, sampleLabel)
				if s.GlobalAV != nil {
					s.GlobalAV.Release()
				}
				out <- avJobResult{index: idx, res: res, err: err}
				if err != nil {
					return
				}
			}
		}()
	}
	go func() {
		wg.Wait()
		close(out)
	}()

	done := 0
	var firstErr error
	for item := range out {
		if item.err != nil {
			if firstErr == nil {
				firstErr = item.err
			}
			continue
		}
		results[item.index] = item.res
		done++
		frac := float64(done) / float64(total)
		overall := progressLo + (progressHi-progressLo)*frac
		if done == 1 || done == total || done%max(1, total/20) == 0 {
			s.progress(fmt.Sprintf("%s %d/%d", progressText, done, total), done, total, "av", overall)
		}
	}
	if firstErr != nil {
		return nil, firstErr
	}
	return results, nil
}

// Run performs deep AV checks on records, mutating their probe fields.
func (s *Service) Run(records []model.Record) error {
	if err := s.checkCancel(); err != nil {
		return err
	}

	// ---- candidate filtering -------------------------------------------------
	candidates := make([]int, 0, len(records))
	for i := range records {
		rec := &records[i]
		if rec.RecordingEnabled == nil || !*rec.RecordingEnabled {
			markSkip(rec, "未配置录像")
			continue
		}
		if rec.DiskStatus != model.StatusOK {
			markSkip(rec, "近期无录像/未知,跳过拉流")
			continue
		}
		if _, ok := numericChannel(rec.Channel); !ok && len(s.AVChannels) == 0 {
			// Without a channel filter there is nothing that could address an
			// invalid channel. With a filter, keep the record so the filter
			// reports its own "not in list" reason (matching Python).
			markSkip(rec, "通道号无效")
			continue
		}
		candidates = append(candidates, i)
	}

	if len(s.AVChannels) > 0 {
		// Filter after real-problem marking (so "未配置录像" is not hidden)
		// and before AVLimit ("--av-channels 31,64 --av-limit 2" means at most
		// two of 31/64).
		allowed := make(map[int]bool, len(s.AVChannels))
		for _, ch := range s.AVChannels {
			allowed[ch] = true
		}
		kept := make([]int, 0, len(candidates))
		for _, idx := range candidates {
			ch, _ := numericChannel(records[idx].Channel)
			if allowed[ch] {
				kept = append(kept, idx)
			} else {
				markSkip(&records[idx], "不在指定抽检通道列表")
			}
		}
		candidates = kept
	}

	if s.AVLimit > 0 && len(candidates) > s.AVLimit {
		overflow := candidates[s.AVLimit:]
		candidates = candidates[:s.AVLimit]
		for _, idx := range overflow {
			markSkip(&records[idx], fmt.Sprintf("超出抽检路数上限(%d)", s.AVLimit))
		}
	}

	if len(candidates) == 0 {
		if len(s.AVChannels) > 0 {
			chs := append([]int(nil), s.AVChannels...)
			sort.Ints(chs)
			parts := make([]string, len(chs))
			for i, ch := range chs {
				parts[i] = strconv.Itoa(ch)
			}
			s.log("深度抽检: 指定通道(通道 " + strings.Join(parts, "、") + ")中没有近期落盘正常的通道")
		} else {
			s.log("深度抽检: 无可用通道(需近期有录像)")
		}
		return nil
	}
	if s.Rec == nil {
		return errors.New("avprobe: Recording is nil")
	}

	saveDir := ""
	if s.AVSave {
		saveDir = s.prepareAVSaveDir()
	}
	avSec := s.avSeconds()
	s.log(fmt.Sprintf("深度音视频抽检: %d 通道 × %ds 视频 + 独立音频轨, 并发 %d (仅短时回放,不写NVR盘)",
		len(candidates), avSec, s.workers()))
	if len(s.AVChannels) > 0 {
		chs := append([]int(nil), s.AVChannels...)
		sort.Ints(chs)
		parts := make([]string, len(chs))
		for i, ch := range chs {
			parts[i] = strconv.Itoa(ch)
		}
		s.log("抽检通道过滤: 仅 " + strings.Join(parts, "、"))
	}
	if saveDir != "" {
		s.log("片段保存目录: " + saveDir)
	}

	// Fixed-instant mode. Service.AVAt is authoritative; fall back to the
	// recording service field so callers that only set it there still work.
	// Warm the device timezone before workers start (mirrors Python; avoids
	// several threads probing /System/status concurrently).
	_ = s.location()
	instants := s.AVAt
	if len(instants) == 0 {
		if rs, ok := s.Rec.(*recording.Service); ok && rs != nil && len(rs.AVAt) > 0 {
			instants = rs.AVAt
		}
	}
	multi := len(instants) > 0
	var clipS, clipE time.Time

	if multi {
		n := len(instants)
		span := (0.90 - 0.70) / float64(n)
		merged := map[int]*probeResult{}
		bestLabel := map[int]string{}
		bestAt := map[int]time.Time{}
		notes := map[int][]string{}
		for idx, atDT := range instants {
			at := atDT
			curS, curE, sampleLabel := s.Rec.PickBusyClipTimes(avSec, &at)
			s.log(fmt.Sprintf("定点抽检 [%d/%d] %s", idx+1, n, sampleLabel))
			text := "深度抽检"
			if n > 1 {
				text = fmt.Sprintf("定点抽检 %d/%d", idx+1, n)
			}
			res, err := s.runAVJobs(records, candidates, curS, curE, sampleLabel,
				0.70+span*float64(idx), 0.70+span*float64(idx+1), text)
			if err != nil {
				return err
			}
			for _, ci := range candidates {
				newRes := res[ci]
				if newRes == nil {
					continue
				}
				notes[ci] = append(notes[ci], fmt.Sprintf("%s=%s/%s",
					curS.Local().Format("15:04"),
					statusOrUnknown(newRes.VideoProbe), statusOrUnknown(newRes.AudioProbe)))
				cur := merged[ci]
				if cur == nil || rankLess(asRank(newRes.VideoProbe, newRes.AudioProbe), asRank(cur.VideoProbe, cur.AudioProbe)) {
					merged[ci] = newRes
					bestLabel[ci] = sampleLabel
					bestAt[ci] = at
				}
			}
		}
		for _, ci := range candidates {
			rec := &records[ci]
			applyAVResult(rec, merged[ci], bestLabel[ci])
			if !bestAt[ci].IsZero() {
				rec.ProbeInstant = bestAt[ci].Local().Format("01-02 15:04:05")
			}
			ns := notes[ci]
			if len(ns) > 1 {
				extra := "多时刻: " + strings.Join(ns, "; ")
				if rec.ProbeDetail != "" {
					rec.ProbeDetail = extra + "; " + rec.ProbeDetail
				} else {
					rec.ProbeDetail = extra
				}
			}
		}
	} else {
		curS, curE, sampleLabel := s.Rec.PickBusyClipTimes(avSec, nil)
		clipS, clipE = curS, curE
		s.log("优先时段: " + sampleLabel)
		res, err := s.runAVJobs(records, candidates, curS, curE, sampleLabel, 0.70, 0.90, "深度抽检")
		if err != nil {
			return err
		}
		s.applyResults(records, candidates, res, sampleLabel)
	}

	// ---- retry ---------------------------------------------------------------
	retry := make([]int, 0)
	for _, ci := range candidates {
		rec := &records[ci]
		if avNeedsRetry(rec.VideoProbe, rec.AudioProbe, rec.ProbeDetail) {
			retry = append(retry, ci)
		}
	}
	if len(retry) > 0 && multi {
		s.log(fmt.Sprintf("定点模式: %d 路仍异常/未确认,不自动换时段复检(需补测可增加 --av-at 时刻)", len(retry)))
		s.progress("深度抽检完成", 0, 0, "av", 0.96)
	} else if len(retry) > 0 {
		rS, rE, rLabel, ok := s.Rec.PickRetryClipTimes(avSec, clipS, clipE)
		if !ok {
			s.log(fmt.Sprintf("初检异常 %d 路, 繁忙窗内无合适换时段,跳过复检", len(retry)))
			s.progress("深度抽检完成", 0, 0, "av", 0.96)
		} else {
			names := make([]string, 0, 8)
			for i, ci := range retry {
				if i >= 8 {
					break
				}
				rec := &records[ci]
				name := rec.Channel
				if name == "" {
					name = rec.TrackID
				}
				if rec.Name != "" && rec.Name != "未知" {
					name += "(" + rec.Name + ")"
				}
				names = append(names, name)
			}
			more := ""
			if len(retry) > 8 {
				more = fmt.Sprintf(" 等%d路", len(retry))
			}
			s.log(fmt.Sprintf("初检异常 %d 路, 换时段复检: %s%s", len(retry), strings.Join(names, "、"), more))
			s.log("复检时段: " + rLabel)
			retryMap, err := s.runAVJobs(records, retry, rS, rE, rLabel, 0.90, 0.96, "异常复检")
			if err != nil {
				return err
			}
			retryOK := 0
			for _, ci := range retry {
				rec := &records[ci]
				newRes := retryMap[ci]
				if newRes == nil {
					continue
				}
				oldV, oldA := rec.VideoProbe, rec.AudioProbe
				oldDetail := rec.ProbeDetail
				if avSeverity(newRes.VideoProbe, newRes.AudioProbe) < avSeverity(oldV, oldA) {
					applyAVResult(rec, newRes, rLabel)
					extra := "复检通过,初检失败"
					if rec.ProbeDetail != "" {
						rec.ProbeDetail = rec.ProbeDetail + "; " + extra
					} else {
						rec.ProbeDetail = extra
					}
					retryOK++
				} else {
					stamp := rS.Local().Format("15:04:05")
					note := newRes.Detail
					if note == "" {
						note = "仍异常"
					}
					if oldDetail != "" {
						rec.ProbeDetail = fmt.Sprintf("%s; 复检@%s仍异常(%s)", oldDetail, stamp, note)
					} else {
						rec.ProbeDetail = fmt.Sprintf("复检@%s仍异常(%s)", stamp, note)
					}
				}
			}
			s.log(fmt.Sprintf("复检通过 %d 路, 仍异常 %d 路", retryOK, len(retry)-retryOK))
		}
	} else {
		s.progress("深度抽检完成", 0, 0, "av", 0.96)
	}

	if s.AVSave {
		savedN := 0
		for _, ci := range candidates {
			if records[ci].SavePath != "" {
				savedN++
			}
		}
		dir := s.AVSaveDir()
		if dir != "" && savedN > 0 {
			s.log(fmt.Sprintf("已保存 %d 个抽检片段 → %s", savedN, dir))
			s.log("时间说明：目录名与文件修改时间是本次巡检启动时刻；文件名末尾的 HHMMSS 才是片段的抽检时刻（设备时区）。核对优先时段请看上方「优先时段」日志")
		} else if dir != "" {
			s.log("已创建目录但未保存到文件: " + dir)
		}
	}
	return nil
}

// ProbeRecord probes one record in the given clip window. It is the single
// channel entry point used by callers that already know the window; when the
// record still carries a playback URI the window is used directly, otherwise
// the recording service is asked to look one up.
func (s *Service) ProbeRecord(rec *model.Record, clipS, clipE time.Time, label string) error {
	if rec == nil {
		return errors.New("avprobe: nil record")
	}
	if err := s.checkCancel(); err != nil {
		return err
	}
	if clipS.IsZero() || clipE.IsZero() {
		if s.Rec != nil {
			curS, curE, curLabel := s.Rec.PickBusyClipTimes(s.avSeconds(), nil)
			clipS, clipE = curS, curE
			if label == "" {
				label = curLabel
			}
		} else if rec.SegStart != nil && rec.SegEnd != nil {
			clipS, clipE = *rec.SegStart, *rec.SegEnd
		} else {
			clipE = util.LatestAVSampleInstant(s.now())
			clipS = clipE.Add(-time.Duration(s.avSeconds()) * time.Second)
		}
	}
	if label == "" {
		label = "抽检点 " + clipS.Local().Format("01-02 15:04:05")
	}
	var (
		res *probeResult
		err error
	)
	if rec.PlaybackURI != "" {
		res, err = s.probeTrackAV(rec.TrackID, rec.PlaybackURI, rec.SegStart, rec.SegEnd,
			rec.HasAudio, &clipS, &clipE, label, rec.Channel, rec.Name, &clipS)
	} else {
		if s.Rec == nil {
			return errors.New("avprobe: Recording is nil")
		}
		res, err = s.probeChannelAt(rec, clipS, clipE, label)
	}
	if err != nil {
		return err
	}
	applyAVResult(rec, res, label)
	return nil
}
