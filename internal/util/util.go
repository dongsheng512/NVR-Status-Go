// Package util contains pure helpers ported from nvr_core/util.py.
package util

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// AVSampleMinAge is how old a playback window must be before probing:
// avoid still-being-written segments that make RTSP time out.
const AVSampleMinAge = 10 * time.Minute

// LatestAVSampleInstant returns now-10m in UTC.
func LatestAVSampleInstant(now time.Time) time.Time {
	return now.UTC().Add(-AVSampleMinAge)
}

// AlternateClipTimes picks another clip start at least 30 minutes away from
// avoidS inside [winS, min(winE, cutoff)]. Ported from util.alternate_clip_times.
func AlternateClipTimes(winS, winE time.Time, seconds int, avoidS, cutoff time.Time) (time.Time, time.Time, bool) {
	hardE := winE
	if cutoff.Before(hardE) {
		hardE = cutoff
	}
	sec := seconds
	if sec < 3 {
		sec = 3
	}
	gap := 30 * time.Minute
	if hardE.Sub(winS) < time.Duration(sec)*time.Second+gap {
		return time.Time{}, time.Time{}, false
	}
	ok := func(s, e time.Time) bool {
		if s.Before(winS) || e.After(hardE) || !e.After(s) {
			return false
		}
		d := s.Sub(avoidS)
		if d < 0 {
			d = -d
		}
		return d >= gap
	}
	deltas := []time.Duration{
		-2 * time.Hour, 2 * time.Hour, -1 * time.Hour, time.Hour,
		-3 * time.Hour, -90 * time.Minute,
	}
	for _, d := range deltas {
		s := avoidS.Add(d)
		e := s.Add(time.Duration(sec) * time.Second)
		if ok(s, e) {
			return s, e, true
		}
	}
	span := hardE.Sub(winS)
	mid := winS.Add(span / 2)
	var s, e time.Time
	if !avoidS.Before(mid) {
		s = winS
		e = s.Add(time.Duration(sec) * time.Second)
	} else {
		e = hardE
		s = e.Add(-time.Duration(sec) * time.Second)
	}
	if ok(s, e) {
		return s, e, true
	}
	return time.Time{}, time.Time{}, false
}

// ToInt converts a string to int, returning def for "" or bad input.
func ToInt(v string, def int) int {
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil {
		return def
	}
	return n
}

// ToFloat converts a string to float64, returning def for "" or bad input.
func ToFloat(v string, def float64) float64 {
	if v == "" {
		return def
	}
	f, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
	if err != nil {
		return def
	}
	return f
}

// ParsePercent parses device-reported percentages such as "98.0%" or "53%".
//
// The bool says whether the text actually was a number. Callers must not treat
// a failed parse as 0: a disk whose usage cannot be read is unknown, not empty.
// Only a trailing "%" is stripped — a stray "%" in the middle means the value is
// malformed, and silently deleting it would turn garbage into a plausible
// number. (health and monitor each had their own copy of this, with different
// semantics; this is the single definition.)
func ParsePercent(s string) (float64, bool) {
	s = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(s), "%"))
	if s == "" {
		return 0, false
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, false
	}
	return v, true
}

var channelSplit = regexp.MustCompile(`[,，、;；\s]+`)

// ParseChannelList parses a channel filter such as "31,64" or "31 64".
// Empty input means no filter. Non-empty input with an invalid token returns an
// error (never silently degrades to "all channels").
//
// It takes a string rather than `any`: the only producers are the CLI flag and
// a record's channel number, both already strings. Callers that hold []int
// (profiles.json) do not need to parse anything.
func ParseChannelList(value string) ([]int, error) {
	items := channelSplit.Split(value, -1)
	seen := map[int]bool{}
	var out []int
	for _, it := range items {
		it = strings.TrimSpace(it)
		if it == "" {
			continue
		}
		n, err := strconv.Atoi(it)
		if err != nil || n <= 0 {
			return nil, fmt.Errorf("通道号无效: %q", it)
		}
		if !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

var instantToken = regexp.MustCompile(`^(\d{1,2})[:：](\d{2})$`)

// ParseInstantList parses "19:05,16:20" into UTC instants on the local day
// `daysAgo` days in the past. Future times are an error.
//
// Only strings are accepted. A caller that already holds []time.Time sets it
// directly (model.ScanOptions.AVAt); routing it back through a parser would
// imply the value needs reparsing, which it does not.
func ParseInstantList(value string, daysAgo int, now time.Time) ([]time.Time, error) {
	if now.IsZero() {
		now = time.Now()
	}
	now = now.Local()

	tokens := channelSplit.Split(value, -1)
	nonEmpty := tokens[:0]
	for _, t := range tokens {
		if strings.TrimSpace(t) != "" {
			nonEmpty = append(nonEmpty, t)
		}
	}
	tokens = nonEmpty
	if len(tokens) == 0 {
		return nil, nil
	}

	seen := map[int64]bool{}
	var out []time.Time
	add := func(t time.Time) {
		k := t.Unix()
		if !seen[k] {
			seen[k] = true
			out = append(out, t.UTC())
		}
	}

	base := now.AddDate(0, 0, -daysAgo)
	for _, tok := range tokens {
		m := instantToken.FindStringSubmatch(strings.TrimSpace(tok))
		if m == nil {
			return nil, fmt.Errorf("定点时刻无效(应为 HH:MM): %q", tok)
		}
		hh, _ := strconv.Atoi(m[1])
		mm, _ := strconv.Atoi(m[2])
		if hh < 0 || hh > 23 || mm < 0 || mm > 59 {
			return nil, fmt.Errorf("定点时刻超出范围: %q", tok)
		}
		local := time.Date(base.Year(), base.Month(), base.Day(), hh, mm, 0, 0, now.Location())
		if local.After(now) {
			return nil, fmt.Errorf("定点时刻 %s 在未来(%s), 该时刻的录像还不存在",
				tok, base.Format("2006-01-02"))
		}
		add(local)
	}
	return out, nil
}

var tzSuffix = regexp.MustCompile(`[+-]\d{2}:\d{2}$`)

// ParseHikTime parses a Hikvision time string into UTC. A value without a
// timezone is interpreted in loc (device local wall clock) when loc is set.
func ParseHikTime(value string, loc *time.Location) *time.Time {
	text := strings.TrimSpace(value)
	if text == "" {
		return nil
	}
	hasZone := strings.HasSuffix(text, "Z") || tzSuffix.MatchString(text)
	if hasZone {
		for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02T15:04:05Z07:00", "2006-01-02 15:04:05Z07:00"} {
			if t, err := time.Parse(layout, text); err == nil {
				u := t.UTC()
				return &u
			}
		}
		return nil
	}
	// No timezone: the device reports local wall clock.
	if loc == nil {
		loc = time.UTC
	}
	for _, layout := range []string{
		"2006-01-02T15:04:05.999999999",
		"2006-01-02T15:04:05",
		"2006-01-02 15:04:05",
	} {
		if t, err := time.ParseInLocation(layout, text, loc); err == nil {
			u := t.UTC()
			return &u
		}
	}
	return nil
}

// Tools locates ffmpeg/ffprobe: bundled bin/ dirs first, then PATH.
func Tools() map[string]string {
	found := map[string]string{"ffmpeg": "", "ffprobe": ""}
	for _, dir := range ToolBinDirs() {
		for _, name := range []string{"ffmpeg", "ffprobe"} {
			if found[name] != "" {
				continue
			}
			for _, exe := range []string{name, name + ".exe"} {
				p := filepath.Join(dir, exe)
				if real, err := filepath.EvalSymlinks(p); err == nil {
					p = real
				}
				if isRunnable(p) {
					found[name] = p
					break
				}
			}
		}
	}
	for _, name := range []string{"ffmpeg", "ffprobe"} {
		if found[name] == "" {
			if p, err := exec.LookPath(name); err == nil {
				found[name] = p
			}
		}
	}
	return found
}

// ToolBinDirs returns candidate directories holding bundled binaries.
func ToolBinDirs() []string {
	var dirs []string
	add := func(p string) {
		if p == "" {
			return
		}
		p = filepath.Clean(p)
		for _, d := range dirs {
			if d == p {
				return
			}
		}
		dirs = append(dirs, p)
	}
	exe, err := os.Executable()
	if err == nil {
		exe, _ = filepath.EvalSymlinks(exe)
		exeDir := filepath.Dir(exe)
		add(filepath.Join(exeDir, "bin"))
		// macOS .app: Contents/MacOS/exe → Contents/Frameworks/bin
		contents := filepath.Dir(exeDir)
		add(filepath.Join(contents, "Frameworks", "bin"))
		add(filepath.Join(contents, "Resources", "bin"))
	}
	if wd, err := os.Getwd(); err == nil {
		add(filepath.Join(wd, "bin"))
		add(filepath.Join(wd, "ffmpeg", "bin"))
		add(filepath.Join(wd, "_internal", "bin"))
	}
	return dirs
}

func isRunnable(path string) bool {
	st, err := os.Stat(path)
	if err != nil || st.IsDir() {
		return false
	}
	return true
}

var unsafeFilename = regexp.MustCompile(`[\\/:*?"<>|\r\n\t]+`)
var whitespace = regexp.MustCompile(`\s+`)
var multiUnderscore = regexp.MustCompile(`_+`)

// SafeFilename returns a filesystem-safe fragment.
func SafeFilename(text string, maxLen int) string {
	s := strings.TrimSpace(text)
	s = unsafeFilename.ReplaceAllString(s, "_")
	s = whitespace.ReplaceAllString(s, "_")
	s = multiUnderscore.ReplaceAllString(s, "_")
	s = strings.Trim(s, "._")
	if s == "" {
		s = "unknown"
	}
	if maxLen > 0 && len([]rune(s)) > maxLen {
		s = string([]rune(s)[:maxLen])
	}
	return s
}

// ErrCancelled is returned when the user cancels a scan.
var ErrCancelled = errors.New("scan cancelled")

// CheckCancel returns ErrCancelled when ctx is done.
func CheckCancel(ctx interface{ Err() error }) error {
	if ctx != nil && ctx.Err() != nil {
		return ErrCancelled
	}
	return nil
}
