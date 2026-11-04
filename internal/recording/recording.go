// Package recording ports nvr_core/recording.py: recording plans, CMSearch
// disk checks and busy-window selection.
package recording

import (
	"context"
	"crypto/rand"
	"fmt"
	"html"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"nvrstatus/internal/model"
	"nvrstatus/internal/util"
	"nvrstatus/internal/xmlnode"
)

// Fetcher is the narrow slice of the ISAPI client this package needs.
type Fetcher interface {
	Get(endpoint string) (string, error)
	Post(endpoint, body string) (string, error)
	DeviceLocation() *time.Location
	Cameras() ([]model.Camera, error)
	Context() context.Context
}

// SearchResult is the outcome of one CMSearch lookback.
type SearchResult struct {
	OK          bool
	Status      string
	Detail      string
	Segments    int
	LatestEnd   *time.Time
	PlaybackURI string
	SegStart    *time.Time
	SegEnd      *time.Time
}

// Service owns recording-plan and CMSearch logic.
type Service struct {
	F                  Fetcher
	LookbackMinutes    int
	SearchWorkers      int
	DeepAVCheck        bool
	CheckDiskRecording bool
	BusyStartHour      int
	BusyEndHour        int
	BusyDaysAgo        int
	AVAt               []time.Time

	Progress func(model.Progress)
	Log      func(msg string)
	Now      func() time.Time

	// DeepCheck is wired by the orchestrator to the avprobe package to avoid
	// an import cycle. It performs the deep audio/video checks in place.
	DeepCheck func([]model.Record) error
}

// CheckCancel reports whether the caller cancelled the run. The context lives
// on the client (installed by scan.Build); this package only borrows it.
func (s *Service) CheckCancel() error { return util.CheckCancel(s.F.Context()) }

var recMode = map[string]string{
	"CMR": "连续录像",
	"MDR": "移动侦测",
	"ADR": "报警录像",
	"AE":  "事件触发",
}

const cmsearchPage = 40

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
		s.Progress(model.Progress{Message: msg, Current: current, Total: total, Phase: phase, Overall: overall})
	}
}

// PhysicalChannel derives the physical channel from a Track node. Track ids
// encode "physical channel + two-digit stream": 101/102 -> 1, 6401/6402 -> 64.
func PhysicalChannel(tr *xmlnode.Node) string {
	if src := strings.TrimSpace(tr.FindText(".//SrcChannel", "")); src != "" {
		return src
	}
	raw := strings.TrimSpace(tr.FindText("Channel", ""))
	if raw == "" {
		raw = strings.TrimSpace(tr.FindText("id", "未知"))
	}
	tid := util.ToInt(raw, -1)
	if tid >= 100 && tid%100 != 0 && (tid%100 == 1 || tid%100 == 2) {
		return strconv.Itoa(tid / 100)
	}
	return raw
}

// SaveAudio returns nil when the device does not expose the field.
func SaveAudio(tr *xmlnode.Node) *bool {
	var walk func(*xmlnode.Node) *bool
	walk = func(n *xmlnode.Node) *bool {
		if n.Name == "SaveAudio" {
			v := strings.ToLower(strings.TrimSpace(n.Text))
			b := v == "true"
			return &b
		}
		for _, c := range n.Children {
			if r := walk(c); r != nil {
				return r
			}
		}
		return nil
	}
	return walk(tr)
}

// RecordingStatus fetches plans, merges main/sub stream tracks and runs the
// recent-disk check.
func (s *Service) RecordingStatus() ([]model.Record, error) {
	text, err := s.F.Get("/ContentMgmt/record/tracks")
	if err != nil || strings.TrimSpace(text) == "" {
		return []model.Record{}, nil
	}
	root, err := xmlnode.Parse(text)
	if err != nil || root.Name == "ResponseStatus" {
		return []model.Record{}, nil
	}
	cameras, _ := s.F.Cameras()
	nameMap := map[string]string{}
	onlineMap := map[string]string{}
	for _, c := range cameras {
		nameMap[c.ID] = c.Name
		onlineMap[c.ID] = c.Online
	}

	var records []model.Record
	for _, tr := range root.FindAll("Track") {
		trackID := tr.FindText("id", "")
		if trackID == "" {
			trackID = tr.FindText("Channel", "未知")
		}
		channel := PhysicalChannel(tr)
		actions := tr.FindAll(".//Actions")
		recActions := 0
		modeCount := map[string]int{}
		for _, a := range actions {
			if a.FindText("Record", "") == "true" {
				recActions++
			}
			if m := a.FindText("ActionRecordingMode", ""); m != "" {
				modeCount[m]++
			}
		}
		total := len(actions)
		enabled := recActions > 0
		mainMode := tr.FindText("DefaultRecordingMode", "未知")
		best := 0
		for m, n := range modeCount {
			if n > best {
				best = n
				mainMode = m
			}
		}
		hasAudio := SaveAudio(tr)
		name := nameMap[channel]
		if name == "" {
			name = "未知"
		}
		online := onlineMap[channel]
		if online == "" {
			online = "unknown"
		}
		rec := model.Record{
			TrackID:           trackID,
			Channel:           channel,
			Name:              name,
			Online:            online,
			RecordingEnabled:  &enabled,
			RecordingMode:     recMode[mainMode],
			RecordingSchedule: fmt.Sprintf("%d/%d", recActions, total),
			HasAudio:          hasAudio,
			DiskStatus:        model.StatusSkip,
			DiskDetail:        "未检查",
			VideoProbe:        model.StatusSkip,
			AudioProbe:        model.StatusSkip,
			ProbeDetail:       "未启用深度抽检",
		}
		if rec.RecordingMode == "" {
			rec.RecordingMode = mainMode
		}
		records = append(records, rec)
	}

	records, merged := mergePhysical(records)
	if merged > 0 {
		s.log(fmt.Sprintf("已按物理通道合并 %d 条重复码流轨道", merged))
	}
	sort.SliceStable(records, func(i, j int) bool {
		return util.ToInt(records[i].Channel, 1<<30) < util.ToInt(records[j].Channel, 1<<30)
	})

	needSearch := s.CheckDiskRecording || s.DeepAVCheck
	diskLo, diskHi := 0.28, 0.92
	if s.DeepAVCheck {
		diskHi = 0.70
	}
	if needSearch && len(records) > 0 {
		toSearch := make([]int, 0, len(records))
		for i := range records {
			if records[i].RecordingEnabled != nil && *records[i].RecordingEnabled {
				toSearch = append(toSearch, i)
			} else {
				records[i].DiskStatus = model.StatusSkip
				records[i].DiskDetail = "未配置录像计划"
			}
		}
		total := len(toSearch)
		s.log(fmt.Sprintf("正在检查近 %d 分钟是否有录像(%d 通道, 并发 %d)...", s.LookbackMinutes, total, s.SearchWorkers))
		s.progress(fmt.Sprintf("近期录像检查 0/%d", total), 0, total, "disk", diskLo)
		s.F.DeviceLocation() // warm timezone before workers

		workers := s.SearchWorkers
		if workers < 1 {
			workers = 1
		}
		type job struct{ idx int }
		jobs := make(chan job)
		results := make([]SearchResult, len(records))
		var wg sync.WaitGroup
		var cancelErr error
		var mu sync.Mutex
		for w := 0; w < workers; w++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for j := range jobs {
					if err := s.CheckCancel(); err != nil {
						mu.Lock()
						cancelErr = err
						mu.Unlock()
						return
					}
					results[j.idx] = s.SearchTrackRecent(records[j.idx].TrackID)
				}
			}()
		}
		go func() {
			defer close(jobs)
			for n, idx := range toSearch {
				if err := s.CheckCancel(); err != nil {
					mu.Lock()
					cancelErr = err
					mu.Unlock()
					return
				}
				jobs <- job{idx: idx}
				done := n + 1
				if done == 1 || done == total || done%max(1, total/25) == 0 {
					frac := float64(done) / float64(total)
					s.progress(fmt.Sprintf("近期录像检查 %d/%d", done, total), done, total, "disk", diskLo+(diskHi-diskLo)*frac)
				}
			}
		}()
		wg.Wait()
		if cancelErr != nil {
			return nil, cancelErr
		}
		for i := range records {
			res := results[i]
			if res.Status == "" {
				res = SearchResult{Status: model.StatusUnknown, Detail: "未返回结果"}
			}
			records[i].DiskStatus = res.Status
			records[i].DiskDetail = res.Detail
			records[i].PlaybackURI = res.PlaybackURI
			records[i].SegStart = res.SegStart
			records[i].SegEnd = res.SegEnd
		}
		s.progress(fmt.Sprintf("近期录像检查完成 %d/%d", total, total), total, total, "disk", diskHi)
	} else {
		for i := range records {
			records[i].DiskStatus = model.StatusSkip
			records[i].DiskDetail = "已跳过近期录像检查"
		}
		s.progress("已跳过近期录像检查", 0, 0, "disk", diskHi)
	}

	if s.DeepAVCheck && s.DeepCheck != nil {
		if err := s.DeepCheck(records); err != nil {
			return nil, err
		}
	}

	for i := range records {
		records[i].RecordingOK = judgeRecordingOK(&records[i])
	}
	return records, nil
}

func mergePhysical(records []model.Record) ([]model.Record, int) {
	merged := map[string]model.Record{}
	var order []string
	var unparsed []model.Record
	for _, r := range records {
		if !isDigits(r.Channel) {
			unparsed = append(unparsed, r)
			continue
		}
		cur, ok := merged[r.Channel]
		if !ok {
			merged[r.Channel] = r
			order = append(order, r.Channel)
			continue
		}
		if util.ToInt(r.TrackID, 1<<30) < util.ToInt(cur.TrackID, 1<<30) {
			merged[r.Channel] = r
		}
	}
	out := make([]model.Record, 0, len(merged)+len(unparsed))
	for _, ch := range order {
		out = append(out, merged[ch])
	}
	out = append(out, unparsed...)
	return out, len(records) - len(out)
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// SearchTrackRecent checks whether a track recorded in the last N minutes.
func (s *Service) SearchTrackRecent(trackID string) SearchResult {
	empty := SearchResult{Status: model.StatusUnknown}
	now := s.now().UTC()
	start := now.Add(-time.Duration(s.LookbackMinutes) * time.Minute)
	end := now.Add(5 * time.Minute)

	matches, codeErr := s.cmsearchCollect(trackID, start, end)
	if codeErr != nil {
		empty.Detail = "检索失败"
		return empty
	}
	if len(matches) == 0 {
		return SearchResult{Status: model.StatusBad, Detail: fmt.Sprintf("近%d分钟无录像片段", s.LookbackMinutes)}
	}
	const lagGrace = 900 * time.Second
	devLoc := s.F.DeviceLocation()

	covers, latest, uri, segS, segE := pickMatches(matches, now, devLoc, lagGrace)
	if !covers && s.LookbackMinutes > 90 {
		recent, _ := s.cmsearchCollect(trackID, now.Add(-90*time.Minute), end)
		if len(recent) > 0 {
			seen := map[string]bool{}
			for _, m := range matches {
				seen[itemKey(m)] = true
			}
			for _, m := range recent {
				if !seen[itemKey(m)] {
					matches = append(matches, m)
					seen[itemKey(m)] = true
				}
			}
			covers, latest, uri, segS, segE = pickMatches(matches, now, devLoc, lagGrace)
		}
	}
	if covers {
		detail := "近期有录像"
		if latest != nil {
			detail += fmt.Sprintf("(至 %s)", latest.Local().Format("15:04:05"))
		}
		return SearchResult{OK: true, Status: model.StatusOK, Detail: detail, Segments: len(matches),
			LatestEnd: latest, PlaybackURI: uri, SegStart: segS, SegEnd: segE}
	}
	endTxt := "未知"
	if latest != nil {
		endTxt = latest.Local().Format("2006-01-02 15:04:05")
	}
	return SearchResult{Status: model.StatusBad, Detail: "仅有较早片段(最晚 " + endTxt + ")",
		Segments: len(matches), LatestEnd: latest, PlaybackURI: uri, SegStart: segS, SegEnd: segE}
}

func pickMatches(matches []*xmlnode.Node, now time.Time, loc *time.Location, grace time.Duration) (bool, *time.Time, string, *time.Time, *time.Time) {
	var latest *time.Time
	covers := false
	var bestURI string
	var bestS, bestE *time.Time
	bestScore := -1.0
	for _, item := range matches {
		st := util.ParseHikTime(item.FindText(".//startTime", ""), loc)
		et := util.ParseHikTime(item.FindText(".//endTime", ""), loc)
		uri := html.UnescapeString(strings.TrimSpace(item.FindText(".//playbackURI", "")))
		if et != nil && (latest == nil || et.After(*latest)) {
			latest = et
		}
		thisCovers := false
		if st != nil && et != nil && !st.After(now) && !now.After(et.Add(grace)) {
			thisCovers = true
		} else if et != nil && !et.Before(now.Add(-grace)) {
			thisCovers = true
		}
		if thisCovers {
			covers = true
		}
		score := 0.0
		if thisCovers {
			score = 1e12
		}
		if et != nil {
			score += float64(et.Unix())
		}
		if uri != "" && score >= bestScore {
			bestScore = score
			bestURI = uri
			bestS, bestE = st, et
		}
	}
	return covers, latest, bestURI, bestS, bestE
}

func (s *Service) cmsearchPage(trackID string, start, end time.Time, maxResults, position int) (int, []*xmlnode.Node, int, error) {
	startS := start.UTC().Format("2006-01-02T15:04:05Z")
	endS := end.UTC().Format("2006-01-02T15:04:05Z")
	searchID := newUUID()
	body := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<CMSearchDescription>
  <searchID>%s</searchID>
  <trackList>
    <trackID>%s</trackID>
  </trackList>
  <timeSpanList>
    <timeSpan>
      <startTime>%s</startTime>
      <endTime>%s</endTime>
    </timeSpan>
  </timeSpanList>
  <maxResults>%d</maxResults>
  <searchResultPostion>%d</searchResultPostion>
</CMSearchDescription>`, searchID, trackID, startS, endS, maxResults, max(0, position))

	text, err := s.F.Post("/ContentMgmt/search", body)
	if err != nil || strings.TrimSpace(text) == "" {
		return 0, nil, 0, err
	}
	root, err := xmlnode.Parse(text)
	if err != nil || root.Name == "ResponseStatus" {
		return 0, nil, 0, err
	}
	matches := root.FindAll(".//searchMatchItem")
	totalRaw := root.FindText(".//totalMatches", "")
	if totalRaw == "" {
		totalRaw = root.FindText("totalMatches", "")
	}
	if totalRaw == "" {
		totalRaw = root.FindText(".//numOfMatches", "")
	}
	total := len(matches)
	if n, err := strconv.Atoi(strings.TrimSpace(totalRaw)); err == nil {
		total = n
	}
	if total < len(matches) {
		total = len(matches)
	}
	return 200, matches, total, nil
}

func (s *Service) cmsearchCollect(trackID string, start, end time.Time) ([]*xmlnode.Node, error) {
	_, matches, total, err := s.cmsearchPage(trackID, start, end, cmsearchPage, 0)
	if err != nil {
		return nil, err
	}
	if total <= len(matches) || total <= cmsearchPage {
		return matches, nil
	}
	lastPos := total - cmsearchPage
	if lastPos < 0 {
		lastPos = 0
	}
	_, more, _, err := s.cmsearchPage(trackID, start, end, cmsearchPage, lastPos)
	if err != nil || len(more) == 0 {
		return matches, nil
	}
	seen := map[string]bool{}
	var merged []*xmlnode.Node
	for _, item := range append(append([]*xmlnode.Node{}, matches...), more...) {
		k := itemKey(item)
		if seen[k] {
			continue
		}
		seen[k] = true
		merged = append(merged, item)
	}
	return merged, nil
}

func itemKey(n *xmlnode.Node) string {
	return strings.TrimSpace(n.FindText(".//playbackURI", "")) + "\x00" +
		strings.TrimSpace(n.FindText(".//startTime", "")) + "\x00" +
		strings.TrimSpace(n.FindText(".//endTime", ""))
}

func newUUID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return strings.ToUpper(fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]))
}

// BusyHoursWindow computes the preferred probe window in UTC.
func (s *Service) BusyHoursWindow() (time.Time, time.Time, string) {
	local := s.now().Local()
	sh, eh := s.BusyStartHour, s.BusyEndHour
	daysAgo := s.BusyDaysAgo
	dayWindow := func(day time.Time) (time.Time, time.Time) {
		start := time.Date(day.Year(), day.Month(), day.Day(), sh, 0, 0, 0, local.Location())
		var end time.Time
		if eh >= 24 {
			end = time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, local.Location()).AddDate(0, 0, 1)
		} else {
			end = time.Date(day.Year(), day.Month(), day.Day(), eh, 0, 0, 0, local.Location())
		}
		return start, end
	}
	if daysAgo > 0 {
		target := local.AddDate(0, 0, -daysAgo)
		ws, we := dayWindow(target)
		label := fmt.Sprintf("%d 天前 %s %02d:00-%02d:00", daysAgo, ws.Format("01-02"), sh, eh)
		if daysAgo == 1 {
			label = fmt.Sprintf("昨天 %s %02d:00-%02d:00", ws.Format("01-02"), sh, eh)
		}
		return ws.UTC(), we.UTC(), label
	}
	todayS, todayE := dayWindow(local)
	if !local.Before(todayS) && local.Before(todayE) {
		ws := todayS
		we := local.Add(-util.AVSampleMinAge)
		if todayE.Before(we) {
			we = todayE
		}
		if !we.After(ws) {
			y := local.AddDate(0, 0, -1)
			ws, we = dayWindow(y)
			return ws.UTC(), we.UTC(), fmt.Sprintf("昨日 %s-%s (今日繁忙可抽检窗不足10分钟)", ws.Format("01-02 15:04"), we.Format("15:04"))
		}
		return ws.UTC(), we.UTC(), fmt.Sprintf("今日 %s-%s (繁忙时段 %02d:00-%02d:00, 距现在≥10分钟)", ws.Format("15:04"), we.Format("15:04"), sh, eh)
	}
	if local.Before(todayS) {
		y := local.AddDate(0, 0, -1)
		ws, we := dayWindow(y)
		return ws.UTC(), we.UTC(), fmt.Sprintf("昨日 %s-%s (当前未到 %02d:00)", ws.Format("01-02 15:04"), we.Format("15:04"), sh)
	}
	ws, we := todayS, todayE
	return ws.UTC(), we.UTC(), fmt.Sprintf("今日 %s-%s (当前已过 %02d:00)", ws.Format("15:04"), we.Format("15:04"), eh)
}

// PickBusyClipTimes chooses the short probe window. at pins a fixed instant.
func (s *Service) PickBusyClipTimes(seconds int, at *time.Time) (time.Time, time.Time, string) {
	ws, we, label := s.BusyHoursWindow()
	local := s.now().Local()
	cutoff := util.LatestAVSampleInstant(s.now())
	peakLocal := time.Date(local.Year(), local.Month(), local.Day(), 14, 0, 0, 0, local.Location())
	wsLocal := ws.Local()
	if wsLocal.YearDay() != local.YearDay() || wsLocal.Year() != local.Year() {
		peakLocal = time.Date(wsLocal.Year(), wsLocal.Month(), wsLocal.Day(), 14, 0, 0, 0, local.Location())
	}
	peak := peakLocal.UTC()
	sec := seconds
	if sec < 3 {
		sec = 3
	}
	var atDT *time.Time
	if at != nil {
		atDT = at
	} else if len(s.AVAt) > 0 {
		atDT = &s.AVAt[0]
	}
	if atDT != nil {
		atS := atDT.Local()
		wantE := atS.Add(time.Duration(sec) * time.Second)
		clipE := wantE
		if cutoff.Before(clipE) {
			clipE = cutoff
		}
		clipS := clipE.Add(-time.Duration(sec) * time.Second)
		head := "定点抽检 " + atS.Format("01-02 15:04:05")
		if clipE.Before(wantE) {
			head += "(距现在不足10分钟,已前移至 "
		} else {
			head += "(抽检点 "
		}
		return clipS, clipE, head + clipS.Local().Format("01-02 15:04:05") + ")"
	}
	var clipS, clipE time.Time
	where := ""
	if !peak.Before(ws) && !peak.After(we) {
		clipE = peak.Add(time.Duration(sec) * time.Second)
		if we.Before(clipE) {
			clipE = we
		}
		if cutoff.Before(clipE) {
			clipE = cutoff
		}
		clipS = clipE.Add(-time.Duration(sec) * time.Second)
		if clipS.Before(ws) {
			clipS = ws
			clipE = clipS.Add(time.Duration(sec) * time.Second)
			if we.Before(clipE) {
				clipE = we
			}
			if cutoff.Before(clipE) {
				clipE = cutoff
			}
		}
		where = "14:00附近"
	} else {
		clipE = we
		if cutoff.Before(clipE) {
			clipE = cutoff
		}
		clipS = clipE.Add(-time.Duration(sec) * time.Second)
		if clipS.Before(ws) {
			clipS = ws
			clipE = clipS.Add(time.Duration(sec) * time.Second)
			if we.Before(clipE) {
				clipE = we
			}
			if cutoff.Before(clipE) {
				clipE = cutoff
			}
		}
		where = "时段末尾"
	}
	if clipE.After(cutoff) {
		clipE = cutoff
		clipS = clipE.Add(-time.Duration(sec) * time.Second)
	}
	return clipS, clipE, fmt.Sprintf("%s; 抽检点 %s(%s)", label, clipS.Local().Format("01-02 15:04:05"), where)
}

// PickRetryClipTimes picks another window after a failed first probe.
func (s *Service) PickRetryClipTimes(seconds int, avoidS, avoidE time.Time) (time.Time, time.Time, string, bool) {
	if len(s.AVAt) > 0 {
		return time.Time{}, time.Time{}, "", false
	}
	ws, we, label := s.BusyHoursWindow()
	cutoff := util.LatestAVSampleInstant(s.now())
	cs, ce, ok := util.AlternateClipTimes(ws, we, seconds, avoidS, cutoff)
	if !ok {
		return time.Time{}, time.Time{}, "", false
	}
	return cs, ce, fmt.Sprintf("%s; 复检点 %s(换时段)", label, cs.Local().Format("01-02 15:04:05")), true
}

// SearchTrackInRange finds a playback URI overlapping [start,end].
func (s *Service) SearchTrackInRange(trackID string, start, end time.Time) SearchResult {
	qStart := start.Add(-5 * time.Minute)
	qEnd := end.Add(5 * time.Minute)
	matches, _ := s.cmsearchCollect(trackID, qStart, qEnd)
	if len(matches) == 0 {
		return SearchResult{Detail: "繁忙时段无录像片段"}
	}
	devLoc := s.F.DeviceLocation()
	var bestURI string
	var bestS, bestE *time.Time
	bestScore := -1.0
	for _, item := range matches {
		st := util.ParseHikTime(item.FindText(".//startTime", ""), devLoc)
		et := util.ParseHikTime(item.FindText(".//endTime", ""), devLoc)
		uri := html.UnescapeString(strings.TrimSpace(item.FindText(".//playbackURI", "")))
		if uri == "" {
			continue
		}
		overlap := 0.0
		if st != nil && et != nil && !st.After(end) && !et.Before(start) {
			overlap = 1e12
		}
		score := overlap
		if et != nil {
			score += float64(et.Unix())
		}
		if score >= bestScore {
			bestScore = score
			bestURI = uri
			bestS, bestE = st, et
		}
	}
	if bestURI == "" {
		return SearchResult{Detail: "繁忙时段无可用回放URI"}
	}
	return SearchResult{OK: true, Detail: "ok", PlaybackURI: bestURI, SegStart: bestS, SegEnd: bestE}
}

// judgeRecordingOK combines plan + disk + deep probe into the row verdict.
func judgeRecordingOK(r *model.Record) string {
	if r.RecordingEnabled == nil || !*r.RecordingEnabled {
		return "未配置"
	}
	disk := r.DiskStatus
	if disk == model.StatusSkip {
		return model.StatusSkip
	}
	if disk == model.StatusBad {
		return model.StatusBad
	}
	if disk != model.StatusOK {
		return model.StatusUnknown
	}
	if r.VideoProbe == model.StatusBad {
		return model.StatusBad
	}
	if r.VideoProbe == model.StatusUnknown && r.ProbeDetail != "" {
		if disk == model.StatusOK {
			return model.StatusOK
		}
		return model.StatusUnknown
	}
	return model.StatusOK
}
