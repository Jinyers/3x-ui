package policyrouting

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"strconv"
	"strings"
	"testing"
)

// Run in a fresh network namespace, never on the developer's own routes:
// XUI_NATIVE_POLICY_E2E=true unshare --net ./policy-routing.test -test.run TestNativePolicyRouting
func TestNativePolicyRouting(t *testing.T) {
	if os.Getenv("XUI_NATIVE_POLICY_E2E") != "true" {
		t.Skip("requires an isolated Linux network namespace with CAP_NET_ADMIN, iproute2 and nftables")
	}
	ifaces, err := net.Interfaces()
	if err != nil || len(ifaces) != 1 || ifaces[0].Name != "lo" {
		t.Fatal("native routing test must start in an isolated network namespace with only loopback")
	}
	ctx := context.Background()
	if !ipv4Forwarding() {
		if err := os.WriteFile("/proc/sys/net/ipv4/ip_forward", []byte("1"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	base := commandRunner{}
	run := func(name string, args ...string) []byte {
		t.Helper()
		out, err := base.Run(ctx, "", name, args...)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	run("ip", "link", "add", "xuiup0", "type", "veth", "peer", "name", "xuiup0p")
	run("ip", "addr", "add", "10.20.0.2/24", "dev", "xuiup0")
	run("ip", "link", "set", "xuiup0", "up")
	run("ip", "link", "set", "xuiup0p", "up")
	run("ip", "link", "add", "xuitun0", "type", "veth", "peer", "name", "xuitun0p")
	run("ip", "link", "set", "xuitun0", "up")
	run("ip", "link", "set", "xuitun0p", "up")
	run("ip", "-4", "route", "add", "default", "via", "10.20.0.1", "dev", "xuiup0")
	run("ip", "-4", "route", "add", "blackhole", "198.18.0.0/15", "table", "10055", "proto", "99")
	run("ip", "-4", "rule", "add", "priority", "20999", "from", "192.0.2.200", "table", "10055")
	// A pre-existing catch-all policy must not intercept our marked packets.
	run("ip", "-4", "route", "add", "blackhole", "default", "table", "10099", "proto", "99")
	run("ip", "-4", "rule", "add", "priority", "9000", "table", "10099")
	run("nft", "add", "table", "inet", "foreign_guard")
	mainBefore := string(run("ip", "-N", "-j", "-4", "route", "show", "table", "main"))
	foreignBefore := string(run("ip", "-N", "-j", "-4", "route", "show", "table", "10055"))
	m := NewManager()
	commands := 0
	m.runner = runnerFunc(func(ctx context.Context, in, name string, args ...string) ([]byte, error) {
		commands++
		return base.Run(ctx, in, name, args...)
	})
	saved := DefaultConfig()
	saves := 0
	load := func() (Config, error) { return saved, nil }
	save := func(c Config) error { saves++; saved = c; return nil }
	c := testConfig()
	if err := m.Update(ctx, c, load, save); err != nil {
		t.Fatal(err)
	}
	if saves != 1 {
		t.Fatalf("configuration saves=%d, want 1", saves)
	}
	before := commands
	for range 3 {
		if err := m.Reconcile(ctx, c); err != nil {
			t.Fatal(err)
		}
	}
	if commands != before || saves != 1 {
		t.Fatal("stable background reconciliation ran commands or saved configuration")
	}
	lookup := func(mark, wantDevice string) {
		t.Helper()
		var routes []map[string]any
		if err := json.Unmarshal(run("ip", "-j", "-4", "route", "get", "203.0.113.1", "mark", mark), &routes); err != nil {
			t.Fatal(err)
		}
		if len(routes) != 1 || routes[0]["dev"] != wantDevice {
			t.Fatalf("mark %s lookup: %v", mark, routes)
		}
	}
	lookup(TunMark, "xuitun0")
	lookup(DirectMark, "xuiup0")
	testIngressPacketMarks(t, base, ctx)
	// A failed replacement must put both kernel state and saved config back.
	fail := true
	m.runner = runnerFunc(func(ctx context.Context, in, name string, args ...string) ([]byte, error) {
		if fail && name == "nft" && strings.Join(args, " ") == "-f -" && strings.Contains(in, "ip daddr 198.51.100.0/24") {
			fail = false
			return nil, errors.New("simulated nft transaction failure")
		}
		return base.Run(ctx, in, name, args...)
	})
	changed := testConfig()
	changed.Rules[0].Destination = "198.51.100.0/24"
	if m.Update(ctx, changed, load, save) == nil {
		t.Fatal("failed kernel update reported success")
	}
	if saved.Rules[0].Destination != c.Rules[0].Destination {
		t.Fatal("failed update left changed configuration saved")
	}
	lookup(TunMark, "xuitun0")
	run("ip", "link", "del", "xuitun0")
	if err := m.Reconcile(ctx, c); err != nil {
		t.Fatal(err)
	}
	if _, err := base.Run(ctx, "", "ip", "-4", "route", "get", "203.0.113.1", "mark", TunMark); err == nil {
		t.Fatal("missing TUN leaked traffic into the main default route")
	}
	if !m.Status(ctx).WaitingForTun {
		t.Fatal("missing TUN is not reported")
	}
	run("ip", "link", "add", "xuitun0", "type", "veth", "peer", "name", "xuitun0p")
	run("ip", "link", "set", "xuitun0", "up")
	if err := m.Reconcile(ctx, c); err != nil {
		t.Fatal(err)
	}
	lookup(TunMark, "xuitun0")
	c.Enabled = false
	if err := m.Update(ctx, c, load, save); err != nil {
		t.Fatal(err)
	}
	if string(run("ip", "-N", "-j", "-4", "route", "show", "table", "main")) != mainBefore || string(run("ip", "-N", "-j", "-4", "route", "show", "table", "10055")) != foreignBefore {
		t.Fatal("unrelated routes changed")
	}
	run("nft", "list", "table", "inet", "foreign_guard")
	if _, err := base.Run(ctx, "", "nft", "list", "table", "inet", TableName); err == nil {
		t.Fatal("disabled policy left its nft table")
	}
	// Recover a crash after saving disabled state but before kernel cleanup.
	c.Enabled = true
	if err := m.Update(ctx, c, load, save); err != nil {
		t.Fatal(err)
	}
	saved.Enabled = false
	m = NewManager()
	if err := m.Reconcile(ctx, saved); err != nil {
		t.Fatal(err)
	}
	if _, err := base.Run(ctx, "", "nft", "list", "table", "inet", TableName); err == nil {
		t.Fatal("restart left a policy active after disabled state was saved")
	}
	// Even a similarly numbered administrator route must never be adopted.
	run("ip", "-4", "route", "add", "blackhole", "default", "table", strconv.Itoa(TunTable), "proto", "99")
	c.Enabled = true
	beforeSaves := saves
	if m.Update(ctx, c, load, save) == nil || saves != beforeSaves {
		t.Fatal("conflicting administrator route was overwritten")
	}
}
