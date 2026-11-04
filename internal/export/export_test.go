package export

import (
	"encoding/csv"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"nvrstatus/internal/model"
)

func boolPtr(v bool) *bool { return &v }

func sampleReport() model.Report {
	return model.Report{
		DeviceName: "NVR-1",
		IP:         "10.0.0.1",
		Info: map[string]string{
			"型号":   "DS-7616",
			"固件版本": "V5.5.71",
		},
		Health: model.HealthSummary{
			Status:   "良好",
			Warnings: []string{},
			Stats: model.HealthStats{
				CameraTotal:  2,
				Online:       2,
				Offline:      0,
				RecordingOK:  2,
				RecordingBad: 0,
				HasAudio:     2,
				DiskOK:       2,
				DiskBad:      0,
			},
		},
		Records: []model.Record{
			{
				Channel:     "1",
				Name:        "大门",
				Online:      "true",
				HasAudio:    boolPtr(true),
				DiskStatus:  "正常",
				RecordingOK: "正常",
				VideoProbe:  "正常",
				AudioProbe:  "正常",
			},
			{
				Channel:     "2",
				Name:        "车库",
				Online:      "false",
				HasAudio:    boolPtr(false),
				DiskStatus:  "跳过",
				RecordingOK: "未知",
				VideoProbe:  "异常",
				AudioProbe:  "警告",
			},
		},
		DiskOverwrite: model.DiskOverwrite{Label: "已开启", Detail: "ISAPI ...", Source: "/ContentMgmt/storage"},
		Drives: []model.Drive{
			{Name: "C", Status: "ok", Usage: "42%", UsedTB: "1", CapacityTB: "3", FreeTB: "2"},
		},
	}
}

func readCSV(t *testing.T, path string) (string, [][]string) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) < 3 || string(raw[:3]) != "\xEF\xBB\xBF" {
		t.Fatalf("file does not start with UTF-8 BOM: %q", raw[:min(3, len(raw))])
	}
	text := string(raw[3:])
	return text, parseCSVText(t, text)
}

func parseCSVText(t *testing.T, text string) [][]string {
	t.Helper()
	reader := csv.NewReader(strings.NewReader(text))
	reader.FieldsPerRecord = -1
	rows, err := reader.ReadAll()
	if err != nil {
		t.Fatalf("parse CSV: %v", err)
	}
	return rows
}

func TestExportCSV(t *testing.T) {
	cases := []struct {
		name       string
		deep       bool
		empty      bool
		wantHeader []string
	}{
		{
			name:       "standard",
			wantHeader: []string{"通道", "名称", "在线", "录像含音频", "落盘状态", "录像是否正常"},
		},
		{
			name:       "deep",
			deep:       true,
			wantHeader: []string{"通道", "名称", "在线", "录像含音频", "落盘状态", "录像是否正常", "视频抽检", "音频抽检"},
		},
		{
			name:       "empty records",
			empty:      true,
			wantHeader: []string{"通道", "名称", "在线", "录像含音频", "落盘状态", "录像是否正常"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			report := sampleReport()
			report.DeepAVCheck = tc.deep
			if tc.empty {
				report.Records = nil
			}
			path := filepath.Join(t.TempDir(), "report.csv")
			if err := ExportCSV(path, report); err != nil {
				t.Fatal(err)
			}
			_, rows := readCSV(t, path)
			if len(rows) == 0 || len(rows[0]) == 0 || rows[0][0] != "# NVR 巡检报告" {
				t.Fatalf("first row = %#v", firstRow(rows))
			}
			headerIndex := findRow(rows, tc.wantHeader)
			if headerIndex < 0 {
				t.Fatalf("header %#v not found in %#v", tc.wantHeader, rows)
			}
			if findRow(rows, []string{"设备名称", "NVR-1"}) < 0 {
				t.Fatal("device name row missing")
			}
			if findRow(rows, []string{"型号", "DS-7616"}) < 0 {
				t.Fatal("model row missing")
			}
			if findRow(rows, []string{"近期有录像", "2"}) < 0 {
				t.Fatal("summary label mapping missing")
			}
			if tc.empty {
				return
			}
			first := rows[headerIndex+1]
			if len(first) != len(tc.wantHeader) {
				t.Fatalf("first record = %#v, want %d columns", first, len(tc.wantHeader))
			}
			if first[1] != "大门" || first[2] != "是" || first[3] != "是" || first[4] != "正常" || first[5] != "正常" {
				t.Fatalf("first record values = %#v", first)
			}
			if tc.deep && (first[6] != "正常" || first[7] != "正常") {
				t.Fatalf("deep probe columns = %#v", first)
			}
			second := rows[headerIndex+2]
			if second[2] != "否" || second[3] != "否" || second[4] != "跳过" || second[5] != "未知" {
				t.Fatalf("second record values = %#v", second)
			}
			if tc.deep && (second[6] != "异常" || second[7] != "警告") {
				t.Fatalf("deep probe columns = %#v", second)
			}
		})
	}
}

func TestCSVFormulaGuard(t *testing.T) {
	prefixes := []string{"=", "+", "-", "@", "\t", "\r"}
	for i, prefix := range prefixes {
		t.Run(fmt.Sprintf("prefix_%d", i), func(t *testing.T) {
			report := sampleReport()
			name := prefix + "payload"
			report.Records = []model.Record{{
				Channel: "1",
				Name:    name,
				Online:  "true",
			}}
			path := filepath.Join(t.TempDir(), "formula.csv")
			if err := ExportCSV(path, report); err != nil {
				t.Fatal(err)
			}
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			text := string(raw[3:])
			// encoding/csv normalizes a bare CR at the start of a cell when
			// UseCRLF is enabled, so compare against the normalized payload.
			payload := strings.TrimLeft(name, "\r")
			if !strings.Contains(text, "'"+payload) {
				t.Fatalf("formula cell %q was not guarded; file: %q", name, text)
			}
			rows := parseCSVText(t, text)
			header := []string{"通道", "名称", "在线", "录像含音频", "落盘状态", "录像是否正常"}
			index := findRow(rows, header)
			if index < 0 {
				t.Fatal("header not found")
			}
			if got := rows[index+1][1]; got != "'"+payload {
				t.Fatalf("formula cell = %q, want %q", got, "'"+payload)
			}
			if rows[index+1][0] != "1" {
				t.Fatalf("plain cell changed: %q", rows[index+1][0])
			}
		})
	}
}

func TestExportCSVIncludesDrivesAndOverwrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "report.csv")
	if err := ExportCSV(path, sampleReport()); err != nil {
		t.Fatal(err)
	}
	_, rows := readCSV(t, path)
	if findRow(rows, []string{"循环覆盖", "已开启"}) < 0 {
		t.Fatal("disk overwrite row missing")
	}
	if findRow(rows, []string{"硬盘", "状态", "使用率", "已用TB", "容量TB", "剩余TB"}) < 0 {
		t.Fatal("drive header missing")
	}
	if findRow(rows, []string{"C", "ok", "42%", "1", "3", "2"}) < 0 {
		t.Fatal("drive row missing")
	}
}

func TestExportTXT(t *testing.T) {
	cases := []struct {
		name       string
		deep       bool
		wantHeader string
	}{
		{
			name:       "standard",
			wantHeader: "通道\t名称\t在线\t录像含音频\t落盘状态\t录像是否正常",
		},
		{
			name:       "deep",
			deep:       true,
			wantHeader: "通道\t名称\t在线\t录像含音频\t落盘状态\t录像是否正常\t视频抽检\t音频抽检",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			report := sampleReport()
			report.DeepAVCheck = tc.deep
			path := filepath.Join(t.TempDir(), "report.txt")
			if err := ExportTXT(path, report, "  健康：良好  \n"); err != nil {
				t.Fatal(err)
			}
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			text := string(raw)
			if !strings.HasPrefix(text, "健康：良好\n\n【通道明细】\n") {
				t.Fatalf("TXT prefix = %q", text)
			}
			if !strings.Contains(text, tc.wantHeader+"\n") {
				t.Fatalf("TXT header missing: %q", text)
			}
			row := "1\t大门\t是\t是\t正常\t正常"
			if tc.deep {
				row += "\t正常\t正常"
			}
			if !strings.Contains(text, row+"\n") {
				t.Fatalf("TXT row %q missing", row)
			}
		})
	}
}

func firstRow(rows [][]string) []string {
	if len(rows) == 0 {
		return nil
	}
	return rows[0]
}

func findRow(rows [][]string, want []string) int {
	for i, row := range rows {
		if len(row) != len(want) {
			continue
		}
		match := true
		for j := range want {
			if row[j] != want[j] {
				match = false
				break
			}
		}
		if match {
			return i
		}
	}
	return -1
}
