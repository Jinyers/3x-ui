package service

import (
	"bytes"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/web/runtime"
	"github.com/mhsanaei/3x-ui/v3/internal/web/service/outbound"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
	"gorm.io/gorm"
)

func TestDisabledMetricsPersistence(t *testing.T) {
	t.Setenv("XUI_DB_FOLDER", t.TempDir())
	t.Setenv("XUI_DISABLE_METRICS_PERSISTENCE", "true")
	previous := systemMetrics
	systemMetrics = newMetricHistory()
	t.Cleanup(func() { systemMetrics = previous })
	systemMetrics.append("cpu", time.Now(), 42)
	if len(systemMetrics.aggregate("cpu", 2, 60)) == 0 {
		t.Fatal("live in-memory history must remain available")
	}
	for range 3 { // Same entry point used by the timer and graceful shutdown.
		if err := PersistSystemMetrics(); err != nil {
			t.Fatal(err)
		}
	}
	for _, path := range []string{systemMetricsStorePath(), systemMetricsStorePath() + ".tmp"} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("disabled persistence created %s: %v", path, err)
		}
	}
	path := systemMetricsStorePath()
	data := []byte("existing snapshot must remain untouched")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	old := time.Unix(100, 0)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	if err := PersistSystemMetrics(); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("existing snapshot modified: %q, %v", got, err)
	}
	info, err := os.Stat(path)
	if err != nil || !info.ModTime().Equal(old) {
		t.Fatalf("existing snapshot timestamp modified: %v", err)
	}
	t.Setenv("XUI_DISABLE_METRICS_PERSISTENCE", "")
	if err := PersistSystemMetrics(); err != nil {
		t.Fatal(err)
	}
	got, err = os.ReadFile(path)
	if err != nil || bytes.Equal(got, data) {
		t.Fatal("default persistence must write a snapshot")
	}
}

func TestDisabledTrafficStatsNoSQLiteChanges(t *testing.T) {
	db := initTrafficTestDB(t)
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	// total_changes is connection-scoped; keep all operations on one connection.
	sqlDB.SetMaxOpenConns(1)
	sqlDB.SetMaxIdleConns(1)
	ib := &model.Inbound{UserId: 1, Tag: "low-write", Enable: true, Port: 43999, Protocol: model.VLESS, Settings: `{"clients":[]}`}
	if err := db.Create(ib).Error; err != nil {
		t.Fatal(err)
	}
	seedClientRow(t, "low-write@example", ib.Id, 0, 0, 0)
	changes := func() int64 {
		t.Helper()
		var n int64
		if err := db.Raw("SELECT total_changes()").Scan(&n).Error; err != nil {
			t.Fatal(err)
		}
		return n
	}
	t.Setenv("XUI_DISABLE_TRAFFIC_STATS", "true")
	in := &InboundService{}
	out := &outbound.OutboundService{}
	traffic := []*xray.Traffic{{Tag: ib.Tag, IsInbound: true, Up: 50, Down: 80}, {Tag: "direct", IsOutbound: true, Up: 50, Down: 80}}
	clients := []*xray.ClientTraffic{{Email: "low-write@example", Up: 50, Down: 80}}
	before := changes()
	for range 3 {
		if _, _, err := in.AddTraffic(traffic, clients); err != nil {
			t.Fatal(err)
		}
		if err, _ := out.AddTraffic(traffic, clients); err != nil {
			t.Fatal(err)
		}
		if err := in.BumpClientsLastOnline([]string{"low-write@example"}); err != nil {
			t.Fatal(err)
		}
		if err := in.AcceptGlobalTraffic("master", clients); err != nil {
			t.Fatal(err)
		}
		if _, err := in.SetRemoteTraffic(1, &runtime.TrafficSnapshot{}, false, false); err != nil {
			t.Fatal(err)
		}
		if err := in.AddTuicTrafficBatch("batch", clients); err != nil {
			t.Fatal(err)
		}
	}
	if after := changes(); after != before {
		t.Fatalf("disabled accounting wrote SQLite rows: %d -> %d", before, after)
	}
	// Configuration still passes through the shared writer while stats are off.
	if err := runSerializedTx(func(tx *gorm.DB) error {
		return tx.Model(ib).Update("remark", "settings still writable").Error
	}); err != nil {
		t.Fatal(err)
	}
	if changes() <= before {
		t.Fatal("configuration writes were disabled")
	}
	before = changes()
	t.Setenv("XUI_DISABLE_TRAFFIC_STATS", "")
	if _, _, err := in.AddTraffic(traffic, clients); err != nil {
		t.Fatal(err)
	}
	if changes() <= before {
		t.Fatal("default traffic accounting did not write")
	}
	var got model.Inbound
	if err := db.First(&got, ib.Id).Error; err != nil {
		t.Fatal(err)
	}
	if got.Up != 50 || got.Down != 80 || got.Remark != "settings still writable" {
		t.Fatalf("unexpected inbound after enabling stats: %+v", got)
	}
}

// Compare generated configs without opening a privileged TUN device on the host.
func TestLowWriteFlagsPreserveTunRoutingConfig(t *testing.T) {
	db := initTrafficTestDB(t)
	ib := &model.Inbound{UserId: 1, Tag: "tun-low-write", Enable: true, Port: 43997,
		Protocol: model.Protocol("tun"), Settings: `{"name":"xray-test","MTU":1500}`, StreamSettings: `{}`, Sniffing: `{}`}
	if err := db.Create(ib).Error; err != nil {
		t.Fatal(err)
	}
	svc := &XrayService{}
	generate := func(flag string) []byte {
		t.Helper()
		t.Setenv("XUI_DISABLE_METRICS_PERSISTENCE", flag)
		t.Setenv("XUI_DISABLE_TRAFFIC_STATS", flag)
		cfg, err := svc.GetXrayConfig()
		if err != nil {
			t.Fatal(err)
		}
		encoded, err := json.Marshal(cfg)
		if err != nil {
			t.Fatal(err)
		}
		return encoded
	}
	normal := generate("")
	disabled := generate("true")
	if !bytes.Equal(normal, disabled) {
		t.Fatalf("flags changed generated TUN/routing/API config")
	}
	if !bytes.Contains(disabled, []byte(`"protocol":"tun"`)) {
		t.Fatal("TUN inbound missing from generated config")
	}
}
