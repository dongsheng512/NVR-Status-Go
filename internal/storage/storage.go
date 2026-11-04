// Package storage ports nvr_core/storage.py: disk status and loop-overwrite
// detection (including the fuzzy firmware field scanning).
package storage

import (
	"fmt"
	"strconv"
	"strings"

	"nvrstatus/internal/model"
	"nvrstatus/internal/util"
	"nvrstatus/internal/xmlnode"
)

// Fetcher is the narrow slice of the ISAPI client this package needs.
type Fetcher interface {
	Get(endpoint string) (string, error)
	CheckCancel() error
}

// Service reads storage status.
type Service struct {
	F   Fetcher
	Log func(string)
}

// quietGetter is implemented by isapi.Client. Capability probes use it so a
// firmware that answers 403 to unsupported endpoints does not fill the scan
// log with warnings.
type quietGetter interface {
	GetQuiet(endpoint string) (string, error)
}

func (s *Service) getQuiet(endpoint string) (string, error) {
	if q, ok := s.F.(quietGetter); ok {
		return q.GetQuiet(endpoint)
	}
	return s.F.Get(endpoint)
}

var overwriteBoolTags = []string{
	"overwrite", "overWrite", "enableOverWrite", "enableOverwrite",
	"isEnableOverWrite", "hddOverWrite", "recordOverWrite", "diskOverWrite",
	"recycleRecord", "cycleCover", "cycleOverwrite", "enableCycleCover",
	"enabledOverwrite",
}

var overwriteStrategyTags = []string{
	"hddFullStrategy", "diskFullStrategy", "hddFull", "fullStrategy",
	"recordFullStrategy", "storageFullStrategy", "overWriteMode",
	"overwriteMode", "diskFullMode", "hddFullMode",
}

// StorageStatus returns the physical drives.
func (s *Service) StorageStatus() ([]model.Drive, error) {
	text, err := s.getQuiet("/ContentMgmt/storage")
	if err != nil || strings.TrimSpace(text) == "" {
		text, err = s.getQuiet("/ContentMgmt/Storage/hdd")
	}
	if err != nil || strings.TrimSpace(text) == "" {
		return []model.Drive{}, nil
	}
	root, err := xmlnode.Parse(text)
	if err != nil {
		return []model.Drive{}, nil
	}
	var drives []model.Drive
	for _, hdd := range root.FindAll(".//hdd") {
		capacity := util.ToInt(hdd.FindText("capacity", ""), 0)
		freeRaw := hdd.FindText("freeSpace", "")
		free := util.ToInt(freeRaw, 0)
		used := 0
		usage := "未知"
		capacityTB := "未知"
		freeTB := "未知"
		usedTB := "未知"
		if capacity > 0 {
			capacityTB = fmt.Sprintf("%.1f", float64(capacity)/1024/1024)
			if strings.TrimSpace(freeRaw) != "" {
				used = capacity - free
				pct := float64(used) / float64(capacity) * 100
				usage = fmt.Sprintf("%.1f%%", pct)
				freeTB = fmt.Sprintf("%.1f", float64(free)/1024/1024)
				usedTB = fmt.Sprintf("%.1f", float64(used)/1024/1024)
			}
		}
		drives = append(drives, model.Drive{
			Name:         hdd.FindText("hddName", "未知"),
			CapacityTB:   capacityTB,
			FreeTB:       freeTB,
			UsedTB:       usedTB,
			Usage:        usage,
			Status:       hdd.FindText("status", "未知"),
			Type:         hdd.FindText("hddType", "未知"),
			Property:     hdd.FindText("property", "未知"),
			Manufacturer: hdd.FindText("manufacturer", "未知"),
		})
	}
	return drives, nil
}

// DiskOverwriteStatus probes the many firmware-specific fields and, when
// none is exposed, infers from nearly-full drives.
func (s *Service) DiskOverwriteStatus(drives []model.Drive) model.DiskOverwrite {
	endpoints := []string{
		"/ContentMgmt/storage",
		"/ContentMgmt/Storage/hdd",
		"/ContentMgmt/Storage/quota",
		"/ContentMgmt/record/AdvanceParam",
		"/ContentMgmt/record/advancedParams",
		"/ContentMgmt/record/advancedParam",
		"/ContentMgmt/Storage/Advanced",
		"/ContentMgmt/Storage/advanced",
	}
	for _, ep := range endpoints {
		if err := s.F.CheckCancel(); err != nil {
			break
		}
		text, err := s.getQuiet(ep)
		if err != nil || strings.TrimSpace(text) == "" {
			continue
		}
		root, err := xmlnode.Parse(text)
		if err != nil {
			continue
		}
		if enabled, tag, raw, ok := scanOverwrite(root); ok {
			label := "未开启"
			if enabled {
				label = "已开启"
			}
			return model.DiskOverwrite{Enabled: &enabled, Label: label,
				Detail: fmt.Sprintf("ISAPI %s · <%s>=%s", ep, tag, raw), Source: ep}
		}
	}
	if drives == nil {
		drives, _ = s.StorageStatus()
	}
	var fullOK, fullBad int
	for _, d := range drives {
		pct := util.ToFloat(strings.ReplaceAll(d.Usage, "%", ""), 0)
		if pct < 95 {
			continue
		}
		st := strings.ToLower(d.Status)
		switch st {
		case "ok", "normal", "idle", "sleep":
			fullOK++
		case "", "未知", "unknown":
		default:
			fullBad++
		}
	}
	if fullOK > 0 && fullBad == 0 {
		b := true
		return model.DiskOverwrite{Enabled: &b, Label: "推断已开启", Source: "infer",
			Detail: "存在使用率≥95% 且状态仍为 ok 的硬盘（循环覆盖时常见）"}
	}
	if fullBad > 0 && fullOK == 0 {
		b := false
		return model.DiskOverwrite{Enabled: &b, Label: "推断未开启", Source: "infer",
			Detail: "存在使用率≥95% 且状态异常的硬盘（未开覆盖时满盘易报错停录）"}
	}
	return model.DiskOverwrite{Label: "未知",
		Detail: "设备未通过 ISAPI 暴露循环覆盖配置，请在 NVR「存储/高级」中人工确认"}
}

func scanOverwrite(root *xmlnode.Node) (bool, string, string, bool) {
	var walk func(*xmlnode.Node) (bool, string, string, bool)
	walk = func(n *xmlnode.Node) (bool, string, string, bool) {
		tag := n.Name
		text := strings.TrimSpace(n.Text)
		if text != "" {
			lower := strings.ToLower(tag)
			for _, name := range overwriteBoolTags {
				if lower == strings.ToLower(name) {
					if b, ok := parseBoolish(text); ok {
						return b, tag, text, true
					}
				}
			}
			for _, name := range overwriteStrategyTags {
				if lower == strings.ToLower(name) {
					if b, ok := parseStrategy(text); ok {
						return b, tag, text, true
					}
				}
			}
			if strings.Contains(lower, "overwrite") || strings.Contains(lower, "over_write") {
				if b, ok := parseBoolish(text); ok {
					return b, tag, text, true
				}
				if b, ok := parseStrategy(text); ok {
					return b, tag, text, true
				}
			}
			if strings.Contains(lower, "fullstrategy") || strings.Contains(lower, "fullmode") || strings.HasSuffix(lower, "hddfull") {
				if b, ok := parseStrategy(text); ok {
					return b, tag, text, true
				}
			}
		}
		for _, c := range n.Children {
			if b, t, raw, ok := walk(c); ok {
				return b, t, raw, ok
			}
		}
		return false, "", "", false
	}
	return walk(root)
}

func parseBoolish(val string) (bool, bool) {
	switch strings.ToLower(strings.TrimSpace(val)) {
	case "true", "1", "yes", "enable", "enabled", "on", "open":
		return true, true
	case "false", "0", "no", "disable", "disabled", "off", "close":
		return false, true
	}
	return false, false
}

func parseStrategy(val string) (bool, bool) {
	v := strings.ToLower(strings.TrimSpace(val))
	if v == "" {
		return false, false
	}
	if strings.Contains(val, "覆盖") || strings.Contains(val, "循环") {
		if strings.Contains(val, "不") || strings.Contains(val, "禁止") || strings.Contains(val, "停止") {
			return false, true
		}
		return true, true
	}
	if strings.Contains(val, "停止") || strings.Contains(val, "停录") {
		return false, true
	}
	if b, ok := parseBoolish(v); ok {
		return b, true
	}
	if strings.Contains(v, "overwrite") || strings.Contains(v, "over_write") || strings.Contains(v, "cover") ||
		strings.Contains(v, "cycle") || strings.Contains(v, "recycle") {
		if strings.Contains(v, "stop") || strings.Contains(v, "disable") || strings.Contains(v, "forbid") ||
			strings.Contains(v, "nooverwrite") || strings.Contains(v, "notoverwrite") {
			return false, true
		}
		return true, true
	}
	if strings.Contains(v, "stop") || strings.Contains(v, "halt") || strings.Contains(v, "forbid") ||
		strings.Contains(v, "norecord") || strings.Contains(v, "do_not") || strings.Contains(v, "donot") {
		return false, true
	}
	return false, false
}

// ParseUsagePct is exposed for tests.
func ParseUsagePct(s string) float64 {
	f, _ := strconv.ParseFloat(strings.TrimSpace(strings.ReplaceAll(s, "%", "")), 64)
	return f
}
