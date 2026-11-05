package job

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/web/runtime"
	"github.com/mhsanaei/3x-ui/v3/internal/web/service"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

func TestLowWriteNodeJobStillImportsConfiguration(t *testing.T) {
	t.Setenv("XUI_DISABLE_TRAFFIC_STATS", "true")
	setupIntegrationDB(t)
	db := database.GetDB()
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(1)
	sqlDB.SetMaxIdleConns(1)
	service.StartTrafficWriter()
	t.Cleanup(service.StopTrafficWriter)
	runtime.SetManager(runtime.NewManager(runtime.LocalDeps{APIPort: func() int { return 0 }, SetNeedRestart: func() {}}))
	t.Cleanup(func() { runtime.SetManager(nil) })
	var polls atomic.Int64
	var ipRequests atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, "clientIps") || strings.HasSuffix(r.URL.Path, "allClientIps") {
			ipRequests.Add(1)
		}
		if strings.HasSuffix(r.URL.Path, "inbounds/list") {
			usage := polls.Add(1) * 100
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "obj": []*model.Inbound{{
				Id: 1, Tag: "remote-in", Port: 443, Protocol: model.VLESS, Enable: true,
				Settings: `{"clients":[{"id":"11111111-2222-4333-8444-555555555555","email":"remote@example","enable":true}]}`,
				Up:       usage, Down: usage,
				ClientStats: []xray.ClientTraffic{{Email: "remote@example", Enable: true, Up: usage, Down: usage, LastOnline: usage}},
			}}})
			return
		}
		_, _ = w.Write([]byte(`{"success":true,"obj":[]}`))
	}))
	t.Cleanup(srv.Close)
	u, _ := url.Parse(srv.URL)
	port, _ := strconv.Atoi(u.Port())
	node := &model.Node{Name: "remote", Guid: "remote-guid", Scheme: "http", Address: u.Hostname(), Port: port, BasePath: "/", ApiToken: "test", Enable: true, Status: "online", AllowPrivateAddress: true}
	if err := db.Create(node).Error; err != nil {
		t.Fatal(err)
	}
	j := NewNodeTrafficSyncJob()
	j.Run()
	j.Run()
	changes := func() int64 {
		t.Helper()
		var n int64
		if err := db.Raw("SELECT total_changes()").Scan(&n).Error; err != nil {
			t.Fatal(err)
		}
		return n
	}
	before := changes()
	for range 3 {
		j.Run()
	}
	var inbounds []model.Inbound
	if err := db.Where("node_id = ?", node.Id).Find(&inbounds).Error; err != nil {
		t.Fatal(err)
	}
	var links int64
	if err := db.Model(&model.ClientInbound{}).Count(&links).Error; err != nil {
		t.Fatal(err)
	}
	if polls.Load() != 5 || len(inbounds) != 1 || links != 1 {
		t.Fatalf("node configuration was not adopted: polls=%d inbounds=%d links=%d", polls.Load(), len(inbounds), links)
	}
	if changes() != before || inbounds[0].Up != 0 || ipRequests.Load() != 0 {
		t.Fatalf("node job persisted statistics or requested IP history: changes=%d->%d up=%d IP requests=%d", before, changes(), inbounds[0].Up, ipRequests.Load())
	}
	if sample := j.prevInboundTotals[inbounds[0].Tag]; sample.up != 500 || sample.down != 500 {
		t.Fatalf("live node counters were disabled with persistence: %+v", sample)
	}
}
