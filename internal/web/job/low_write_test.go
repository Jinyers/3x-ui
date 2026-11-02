package job

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/config"
	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/dbtest"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

func TestDisabledTrafficStatsKeepsTuicJournal(t *testing.T) {
	t.Setenv("XUI_DB_FOLDER", t.TempDir())
	t.Setenv("XUI_DISABLE_TRAFFIC_STATS", "true")
	if err := os.MkdirAll(tuicJournalDir(), 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(tuicJournalDir(), "existing.json")
	want := []byte("retained journal, not decoded while accounting is off")
	if err := os.WriteFile(path, want, 0600); err != nil {
		t.Fatal(err)
	}
	j := NewTuicJob()
	if err := j.replayTuicJournal(); err != nil {
		t.Fatal(err)
	}
	if err := j.FlushStoppedTraffic(); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("existing journal changed: %v", err)
	}
	entries, err := os.ReadDir(tuicJournalDir())
	if err != nil || len(entries) != 1 {
		t.Fatalf("shutdown created journal files: %v", err)
	}
	t.Setenv("XUI_DISABLE_TRAFFIC_STATS", "")
	if err := j.replayTuicJournal(); err == nil {
		t.Fatal("enabled replay should try to decode the existing journal")
	}
}

func TestDisabledTrafficStatsSkipsScheduledReset(t *testing.T) {
	t.Setenv("XUI_DB_FOLDER", t.TempDir())
	dbtest.InitDB(t, config.GetDBPath())
	db := database.GetDB()
	ib := &model.Inbound{UserId: 1, Tag: "scheduled-reset", Enable: true, Port: 43998, Protocol: model.VLESS, Settings: `{"clients":[]}`, Up: 123, TrafficReset: "hourly"}
	if err := db.Create(ib).Error; err != nil {
		t.Fatal(err)
	}
	j := NewPeriodicTrafficResetJob("hourly", time.UTC)
	t.Setenv("XUI_DISABLE_TRAFFIC_STATS", "true")
	j.Run()
	var got model.Inbound
	if err := db.First(&got, ib.Id).Error; err != nil {
		t.Fatal(err)
	}
	if got.Up != 123 {
		t.Fatal("disabled stats still performed a scheduled reset")
	}
	t.Setenv("XUI_DISABLE_TRAFFIC_STATS", "")
	j.Run()
	if err := db.First(&got, ib.Id).Error; err != nil {
		t.Fatal(err)
	}
	if got.Up != 0 {
		t.Fatal("default scheduled reset stopped working")
	}
}
