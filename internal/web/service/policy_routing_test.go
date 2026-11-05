package service

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/policyrouting"
)

func TestPolicyRoutingSettingsAreCompatibleAndReadOnlyUntilApply(t *testing.T) {
	db := initTrafficTestDB(t)
	changes := sqliteChanges(t, db)
	t.Setenv("XUI_ENABLE_POLICY_ROUTING", "false")
	t.Setenv("XUI_DISABLE_TRAFFIC_STATS", "true")
	cfg := policyrouting.DefaultConfig()
	cfg.Enabled = true
	cfg.Rules = []policyrouting.Rule{{Enabled: true, Name: "LAN HTTPS", Source: "192.0.2.0/24", Protocol: "tcp", DestinationPort: "443", Action: "tun"}}
	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	// The explicit setting write remains functional in low-write mode.
	if err := (&SettingService{}).saveSetting(policyRoutingSetting, string(data)); err != nil {
		t.Fatal(err)
	}
	before := changes()
	svc := &PolicyRoutingService{}
	for range 3 {
		v, err := svc.View(context.Background())
		if err != nil || v.EnabledByEnv || v.State.Ready || len(v.Config.Rules) != 1 || v.Config.Rules[0].Source != cfg.Rules[0].Source {
			t.Fatalf("unexpected policy view: %+v, %v", v, err)
		}
		if err := svc.Reconcile(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if err := svc.Apply(context.Background(), cfg); err == nil {
		t.Fatal("kernel application was allowed without its opt-in flag")
	}
	if _, err := (&SettingService{}).GetAllSetting(); err != nil {
		t.Fatalf("native setting broke upstream settings reader: %v", err)
	}
	if changes() != before {
		t.Fatal("view/reconcile or rejected application wrote SQLite")
	}
}
