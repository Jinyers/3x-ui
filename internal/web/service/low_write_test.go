package service

import (
	"bytes"
	"encoding/json"
	"os"
	"testing"
	"time"

	"gorm.io/gorm"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/web/runtime"
	"github.com/mhsanaei/3x-ui/v3/internal/web/service/outbound"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
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
	if err := os.WriteFile(path, data, 0o600); err != nil {
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

func sqliteChanges(t *testing.T, db *gorm.DB) func() int64 {
	t.Helper()
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(1)
	sqlDB.SetMaxIdleConns(1)
	return func() int64 {
		t.Helper()
		var n int64
		if err := db.Raw("SELECT total_changes()").Scan(&n).Error; err != nil {
			t.Fatal(err)
		}
		return n
	}
}

func TestLowWriteNodeConfigAdoptionWithoutAccounting(t *testing.T) {
	db := initTrafficTestDB(t)
	changes := sqliteChanges(t, db)
	t.Setenv("XUI_DISABLE_TRAFFIC_STATS", "true")
	node := &model.Node{Name: "remote", Guid: "remote-guid", Address: "192.0.2.10", Port: 2053, Enable: true}
	if err := db.Create(node).Error; err != nil {
		t.Fatal(err)
	}
	ib := &model.Inbound{
		Tag: "remote-vless", Port: 443, Enable: true, Protocol: model.VLESS,
		Settings: `{"clients":[{"id":"11111111-2222-4333-8444-555555555555","email":"remote@example","enable":true}]}`,
		Up:       100, Down: 200,
		ClientStats: []xray.ClientTraffic{{Email: "remote@example", Enable: true, Up: 100, Down: 200, LastOnline: 1000}},
	}
	snap := &runtime.TrafficSnapshot{Inbounds: []*model.Inbound{ib}, OnlineEmails: []string{"remote@example"}}
	svc := &InboundService{}
	merge := func() {
		t.Helper()
		if _, err := svc.SetRemoteTraffic(node.Id, snap, false, false); err != nil {
			t.Fatal(err)
		}
	}
	merge() // Initial adoption is a configuration write.
	var stored model.Inbound
	if err := db.Where("node_id = ?", node.Id).First(&stored).Error; err != nil {
		t.Fatal(err)
	}
	if stored.Up != 0 || stored.Down != 0 || len(linksOf(t, stored.Id)) != 1 {
		t.Fatal("adoption must retain client configuration without importing usage")
	}
	merge() // Allow the normal adoption lifecycle to converge.
	before := changes()
	for range 3 {
		ib.Up += 100
		ib.Down += 200
		ib.ClientStats[0].Up += 100
		ib.ClientStats[0].LastOnline++
		merge()
	}
	if after := changes(); after != before {
		t.Fatalf("steady node polls wrote SQLite: %d -> %d", before, after)
	}
	ct := readTraffic(t, db, "remote@example")
	if ct.Up != 0 || ct.Down != 0 || ct.LastOnline != 0 {
		t.Fatalf("accounting was not frozen: %+v", ct)
	}
	ib.Remark = "edited remotely"
	ib.Settings = `{"clients":[{"id":"11111111-2222-4333-8444-555555555555","email":"remote@example","enable":true},{"id":"21111111-2222-4333-8444-555555555555","email":"second@example","enable":true}]}`
	merge()
	if err := db.First(&stored, stored.Id).Error; err != nil {
		t.Fatal(err)
	}
	if stored.Remark != ib.Remark || len(linksOf(t, stored.Id)) != 2 || changes() <= before {
		t.Fatal("remote configuration edits must still be imported and persisted")
	}
}

func TestLowWriteHeartbeatKeepsLiveMetricsAndNodeState(t *testing.T) {
	db := initTrafficTestDB(t)
	changes := sqliteChanges(t, db)
	t.Setenv("XUI_DISABLE_METRICS_PERSISTENCE", "true")
	node := &model.Node{Name: "remote", Address: "192.0.2.10", Port: 2053, Enable: true}
	if err := db.Create(node).Error; err != nil {
		t.Fatal(err)
	}
	svc := &NodeService{}
	p := HeartbeatPatch{Guid: "remote-guid", Status: "online", XrayState: "running", PanelVersion: "v3", LastHeartbeat: time.Now().Unix(), CpuPct: 1, NetUp: 100}
	if err := svc.UpdateHeartbeat(node.Id, p); err != nil {
		t.Fatal(err)
	}
	before := changes()
	for range 3 {
		p.LastHeartbeat++
		p.CpuPct++
		p.LatencyMs++
		p.NetUp++
		if err := svc.UpdateHeartbeat(node.Id, p); err != nil {
			t.Fatal(err)
		}
	}
	if after := changes(); after != before {
		t.Fatalf("heartbeat metrics wrote SQLite: %d -> %d", before, after)
	}
	got, err := svc.GetById(node.Id)
	if err != nil || got.LastHeartbeat != p.LastHeartbeat || got.CpuPct != p.CpuPct || got.NetUp != p.NetUp {
		t.Fatalf("live node metrics missing: %+v, %v", got, err)
	}
	all, err := svc.GetAll()
	if err != nil || len(all) != 1 || all[0].LatencyMs != p.LatencyMs {
		t.Fatalf("node list lost live metrics: %+v, %v", all, err)
	}
	descendants, err := svc.LocalDescendants()
	if err != nil || len(descendants) != 1 || descendants[0].LastHeartbeat != p.LastHeartbeat {
		t.Fatalf("node API lost live metrics: %+v, %v", descendants, err)
	}
	if len(svc.AggregateNodeMetric(node.Id, "netUp", 2, 60)) == 0 {
		t.Fatal("live node metric history missing")
	}
	p.Status, p.LastError = "offline", "connection refused"
	if err := svc.UpdateHeartbeat(node.Id, p); err != nil {
		t.Fatal(err)
	}
	_, status, _, _, err := svc.NodeSyncState(node.Id)
	if err != nil || status != "offline" || changes() <= before {
		t.Fatal("real health transitions must remain visible to reconciliation")
	}
}

func TestLowWriteUnlimitedHwidDoesNotTrackDevices(t *testing.T) {
	initClientHwidTestDB(t)
	db := database.GetDB()
	changes := sqliteChanges(t, db)
	rec := seedHwidClient(t, 0)
	t.Setenv("XUI_DISABLE_TRAFFIC_STATS", "true")
	before := changes()
	for range 3 {
		res, err := (&ClientService{}).EnforceHwidForSubID(rec.SubID, HwidRequest{Hwid: "device-one", UserAgent: "Happ/1.0"})
		if err != nil || !res.Allowed || res.Active {
			t.Fatalf("unlimited HWID response: %+v, %v", res, err)
		}
	}
	if changes() != before {
		t.Fatal("unlimited HWID history wrote SQLite")
	}
	if err := db.Model(rec).Update("limit_hwid", 1).Error; err != nil {
		t.Fatal(err)
	}
	res, err := (&ClientService{}).EnforceHwidForSubID(rec.SubID, HwidRequest{Hwid: "device-one"})
	if err != nil || !res.Allowed || !res.Active {
		t.Fatalf("explicit HWID limit must still work: %+v, %v", res, err)
	}
	res, err = (&ClientService{}).EnforceHwidForSubID(rec.SubID, HwidRequest{Hwid: "device-two"})
	if err != nil || res.Allowed || !res.MaxDevicesReached {
		t.Fatalf("explicit HWID limit bypassed: %+v, %v", res, err)
	}
}

// Compare generated configs without opening a privileged TUN device on the host.
func TestLowWriteFlagsPreserveTunRoutingConfig(t *testing.T) {
	db := initTrafficTestDB(t)
	ib := &model.Inbound{
		UserId: 1, Tag: "tun-low-write", Enable: true, Port: 43997,
		Protocol: model.Protocol("tun"), Settings: `{"name":"xray-test","MTU":1500}`, StreamSettings: `{}`, Sniffing: `{}`,
	}
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
