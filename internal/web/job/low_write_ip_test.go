package job

import (
	"crypto/sha256"
	"os"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/config"
	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

// The HTTP smoke test has no named clients; exercise the real IP job separately.
func TestDisabledTrafficStatsSkipsCollectionOnlyIpWrites(t *testing.T) {
	for _, tc := range []struct {
		name     string
		fail2ban string
		limit    int
	}{
		{name: "enforcement-disabled", fail2ban: "false", limit: 1},
		{name: "no-client-limits", fail2ban: "true", limit: 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			setupIntegrationDB(t)
			t.Setenv("XUI_DISABLE_METRICS_PERSISTENCE", "true")
			t.Setenv("XUI_DISABLE_TRAFFIC_STATS", "true")
			t.Setenv("XUI_ENABLE_FAIL2BAN", tc.fail2ban)
			const email = "low-write-ip@example"
			seedLinkedInboundWithClient(t, "low-write-ip", email, tc.limit)
			// Also exercise the five-minute stale-IP pruning path.
			seedClientIps(t, "stale@example", []model.ClientIpEntry{{IP: "198.51.100.1", Timestamp: 1}})
			db := database.GetDB()
			sqlDB, err := db.DB()
			if err != nil {
				t.Fatal(err)
			}
			sqlDB.SetMaxOpenConns(1)
			sqlDB.SetMaxIdleConns(1)
			changes := func() int64 {
				t.Helper()
				var n int64
				if err := db.Raw("SELECT total_changes()").Scan(&n).Error; err != nil {
					t.Fatal(err)
				}
				return n
			}
			walHash := func() [32]byte {
				t.Helper()
				data, err := os.ReadFile(config.GetDBPath() + "-wal")
				if err != nil {
					t.Fatal(err)
				}
				return sha256.Sum256(data)
			}
			j := NewCheckClientIpJob()
			j.onlineUsers = staticOnlineUsers{{Email: email, IPs: []xray.OnlineIP{{IP: "203.0.113.20"}}}}
			before, beforeWAL := changes(), walHash()
			for range 3 {
				j.Run()
			}
			if after := changes(); after != before {
				t.Fatalf("IP job still wrote rows: %d -> %d", before, after)
			}
			if walHash() != beforeWAL {
				t.Fatal("IP job changed the WAL")
			}
			// The standard mode must keep recording IPs even without enforcement.
			t.Setenv("XUI_DISABLE_TRAFFIC_STATS", "")
			j.Run()
			if changes() <= before {
				t.Fatal("default IP tracking no longer writes")
			}
			if len(readClientIps(t, email)) != 1 {
				t.Fatal("default IP tracking lost observations")
			}
		})
	}
}

func TestDisabledTrafficStatsPreservesEnabledIpLimit(t *testing.T) {
	setupIntegrationDB(t)
	t.Setenv("XUI_DISABLE_TRAFFIC_STATS", "true")
	t.Setenv("XUI_ENABLE_FAIL2BAN", "true")
	fakeFail2BanClient(t)
	const email = "limited-low-write@example"
	seedLinkedInboundWithClient(t, "limited-low-write", email, 1)
	now := time.Now().Unix()
	j := NewCheckClientIpJob()
	j.onlineUsers = staticOnlineUsers{{Email: email, IPs: []xray.OnlineIP{
		{IP: "203.0.113.20", LastSeen: now - 1},
		{IP: "203.0.113.21", LastSeen: now},
	}}}
	j.Run() // First scan seeds the row; the second enforces its configured limit.
	j.Run()
	if banLineCount(t, email) == 0 {
		t.Fatal("traffic flag disabled configured IP-limit enforcement")
	}
}
