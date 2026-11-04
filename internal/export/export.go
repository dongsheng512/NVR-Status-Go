// Package export writes inspection reports as Excel-friendly CSV (UTF-8 with
// BOM) and as plain text. It is a Go port of services/export_report.py.
package export

import (
	"encoding/csv"
	"fmt"
	"os"
	"strings"
	"time"

	"nvrstatus/internal/model"
)

// CSV formula-injection prefixes. Excel/WPS executes text cells starting with
// any of these strings; such cells are prefixed with a single quote.
var formulaPrefixes = []string{"=", "+", "-", "@", "\t", "\r"}

// ExportCSV writes the report as UTF-8 CSV with a BOM. The record header uses
// the same columns as Python's _row_values; deep reports add the two probe
// columns.
func ExportCSV(path string, r model.Report) error {
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("创建 CSV 文件失败: %w", err)
	}
	defer f.Close()

	if _, err := f.WriteString("\xEF\xBB\xBF"); err != nil {
		return fmt.Errorf("写入 CSV BOM 失败: %w", err)
	}
	w := csv.NewWriter(f)
	w.UseCRLF = true
	defer w.Flush()

	write := func(cells ...any) error {
		row := make([]string, len(cells))
		for i, cell := range cells {
			row[i] = safeCell(cell)
		}
		if err := w.Write(row); err != nil {
			return fmt.Errorf("写入 CSV 行失败: %w", err)
		}
		return nil
	}
	writeAll := func(rows [][]any) error {
		for _, row := range rows {
			if err := write(row...); err != nil {
				return err
			}
		}
		return nil
	}

	if err := writeAll([][]any{
		{"# NVR 巡检报告"},
		{"导出时间", time.Now().Format("2006-01-02 15:04:05")},
		{"设备名称", r.DeviceName},
		{"型号", r.Info["型号"]},
		{"固件", r.Info["固件版本"]},
		{"健康状态", r.Health.Status},
	}); err != nil {
		return err
	}
	for i, warning := range r.Health.Warnings {
		if err := write(fmt.Sprintf("预警%d", i+1), warning); err != nil {
			return err
		}
	}
	if err := write(); err != nil {
		return err
	}
	if err := write("汇总项", "数值"); err != nil {
		return err
	}
	for _, item := range summaryRows(r) {
		if err := write(item[0], item[1]); err != nil {
			return err
		}
	}
	if err := write(); err != nil {
		return err
	}

	header := []any{"通道", "名称", "在线", "录像含音频", "落盘状态", "录像是否正常"}
	if r.DeepAVCheck {
		header = append(header, "视频抽检", "音频抽检")
	}
	if err := write(header...); err != nil {
		return err
	}
	for _, record := range r.Records {
		values := recordValues(record, r.DeepAVCheck)
		cells := make([]any, len(values))
		for i, value := range values {
			cells[i] = value
		}
		if err := write(cells...); err != nil {
			return err
		}
	}

	if r.DiskOverwrite.Label != "" || r.DiskOverwrite.Detail != "" || r.DiskOverwrite.Source != "" {
		if err := write(); err != nil {
			return err
		}
		if err := write("循环覆盖", orDefault(r.DiskOverwrite.Label, "未知")); err != nil {
			return err
		}
		if r.DiskOverwrite.Detail != "" {
			if err := write("循环覆盖说明", r.DiskOverwrite.Detail); err != nil {
				return err
			}
		}
		if r.DiskOverwrite.Source != "" {
			if err := write("循环覆盖来源", r.DiskOverwrite.Source); err != nil {
				return err
			}
		}
	} else if r.Health.Stats.OverwriteLabel != "" {
		if err := write(); err != nil {
			return err
		}
		if err := write("循环覆盖", r.Health.Stats.OverwriteLabel); err != nil {
			return err
		}
	}

	if len(r.Drives) > 0 {
		if err := write(); err != nil {
			return err
		}
		if err := write("硬盘", "状态", "使用率", "已用TB", "容量TB", "剩余TB"); err != nil {
			return err
		}
		for _, drive := range r.Drives {
			if err := write(drive.Name, drive.Status, drive.Usage, drive.UsedTB, drive.CapacityTB, drive.FreeTB); err != nil {
				return err
			}
		}
	}

	w.Flush()
	if err := w.Error(); err != nil {
		return fmt.Errorf("写入 CSV 失败: %w", err)
	}
	return nil
}

// ExportTXT writes a plain-text report: a free-form summary, followed by the
// per-channel table.
func ExportTXT(path string, r model.Report, summary string) error {
	lines := []string{strings.TrimSpace(summary), "", "【通道明细】"}
	header := []string{"通道", "名称", "在线", "录像含音频", "落盘状态", "录像是否正常"}
	if r.DeepAVCheck {
		header = append(header, "视频抽检", "音频抽检")
	}
	lines = append(lines, strings.Join(header, "\t"))
	for _, record := range r.Records {
		lines = append(lines, strings.Join(recordValues(record, r.DeepAVCheck), "\t"))
	}
	content := strings.Join(lines, "\n") + "\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return fmt.Errorf("写入 TXT 失败: %w", err)
	}
	return nil
}

// summaryRows returns the ordered 汇总项 rows, using the same friendly labels
// as the Python exporter.
func summaryRows(r model.Report) [][2]any {
	stats := r.Health.Stats
	items := []struct {
		key   string
		value any
	}{
		{"摄像头总数", stats.CameraTotal},
		{"摄像头在线", stats.Online},
		{"摄像头离线", stats.Offline},
		{"计划已配置", stats.PlanConfigured},
		{"计划未配置", stats.PlanMissing},
		{"录像正常", stats.RecordingOK},
		{"录像异常", stats.RecordingBad},
		{"录像未知", stats.RecordingUnknown},
		{"含音频", stats.HasAudio},
		{"不含音频", stats.NoAudio},
		{"落盘正常", stats.DiskOK},
		{"落盘异常", stats.DiskBad},
		{"落盘未知", stats.DiskUnknown},
		{"视频抽检正常", stats.VideoProbeOK},
		{"视频抽检异常", stats.VideoProbeBad},
		{"音频抽检正常", stats.AudioProbeOK},
		{"音频抽检异常", stats.AudioProbeBad},
		{"音频抽检警告", stats.AudioProbeWarn},
		{"音频抽检未知", stats.AudioProbeUnknown},
	}
	labels := map[string]string{
		"落盘正常": "近期有录像",
		"落盘异常": "近期无录像",
		"落盘未知": "近期录像未知",
	}
	rows := make([][2]any, 0, len(items))
	for _, item := range items {
		label := item.key
		if friendly, ok := labels[label]; ok {
			label = friendly
		}
		rows = append(rows, [2]any{label, item.value})
	}
	return rows
}

// recordValues renders one record into the Python _row_values column order.
func recordValues(r model.Record, deep bool) []string {
	row := []string{
		r.Channel,
		r.Name,
		formatOnline(r.Online),
		formatBool(r.HasAudio),
		formatStatus(r.DiskStatus),
		formatStatus(r.RecordingOK),
	}
	if deep {
		row = append(row, formatStatus(r.VideoProbe), formatStatus(r.AudioProbe))
	}
	return row
}

func formatOnline(value string) string {
	switch value {
	case "true":
		return "是"
	case "false":
		return "否"
	default:
		return "—"
	}
}

func formatBool(value *bool) string {
	if value == nil {
		return "—"
	}
	if *value {
		return "是"
	}
	return "否"
}

func formatStatus(value string) string {
	if value == "" {
		return "—"
	}
	return value
}

// safeCell escapes CSV formula injection: text beginning with = + - @ TAB or
// CR gets a leading apostrophe. Non-string values (numbers etc.) are passed
// through unchanged, matching the Python implementation.
func safeCell(value any) string {
	text, ok := value.(string)
	if !ok {
		return fmt.Sprint(value)
	}
	for _, prefix := range formulaPrefixes {
		if strings.HasPrefix(text, prefix) {
			return "'" + text
		}
	}
	return text
}

func orDefault(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}
