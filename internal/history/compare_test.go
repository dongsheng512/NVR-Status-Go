package history

import (
	"testing"

	"nvrstatus/internal/model"
)

func rec(ch, name string, ok string, online string) model.Record {
	return model.Record{Channel: ch, TrackID: ch, Name: name, RecordingOK: ok, Online: online}
}

func TestCompareNewResolvedStill(t *testing.T) {
	prev := model.Report{Health: model.HealthSummary{Status: model.StatusOK}, Records: []model.Record{
		rec("1", "前门", model.StatusOK, "true"),
		rec("2", "后门", model.StatusOK, "true"),
		rec("3", "仓库", model.StatusBad, "true"),
	}}
	cur := model.Report{Health: model.HealthSummary{Status: model.StatusWarn}, Records: []model.Record{
		rec("1", "前门", model.StatusOK, "true"),
		rec("2", "后门", model.StatusBad, "false"),
		rec("3", "仓库", model.StatusBad, "true"),
	}}
	d := Compare(prev, cur)
	if d.HealthBefore != model.StatusOK || d.HealthAfter != model.StatusWarn {
		t.Fatalf("health = %s → %s", d.HealthBefore, d.HealthAfter)
	}
	if len(d.NewIssues) != 1 || len(d.Resolved) != 0 || len(d.StillBad) != 1 {
		t.Fatalf("diff = %+v", d)
	}
	if d.Summary() == "" {
		t.Fatal("empty summary")
	}
}

func TestCompareResolved(t *testing.T) {
	prev := model.Report{Health: model.HealthSummary{Status: model.StatusBad}, Records: []model.Record{
		rec("1", "前门", model.StatusBad, "false"),
	}}
	cur := model.Report{Health: model.HealthSummary{Status: model.StatusOK}, Records: []model.Record{
		rec("1", "前门", model.StatusOK, "true"),
	}}
	d := Compare(prev, cur)
	if len(d.Resolved) != 1 || len(d.NewIssues) != 0 {
		t.Fatalf("diff = %+v", d)
	}
}

func TestLatestForDevice(t *testing.T) {
	dir := setupHistory(t)
	// setupHistory returns reports dir; SaveReport writes there.
	if _, err := SaveReport(model.Report{DeviceName: "A", IP: "10.0.0.1", Health: model.HealthSummary{Status: model.StatusOK}}); err != nil {
		t.Fatal(err)
	}
	if _, err := SaveReport(model.Report{DeviceName: "B", IP: "10.0.0.2", Health: model.HealthSummary{Status: model.StatusWarn}}); err != nil {
		t.Fatal(err)
	}
	rep, err := LatestForDevice("10.0.0.1")
	if err != nil || rep == nil || rep.DeviceName != "A" {
		t.Fatalf("rep=%+v err=%v dir=%s", rep, err, dir)
	}
}
