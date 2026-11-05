package policyrouting

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func testConfig() Config {
	c := DefaultConfig()
	c.Enabled, c.Ingress, c.Tun, c.Uplink, c.Gateway = true, "xuiup0", "xuitun0", "xuiup0", "10.20.0.1"
	c.Rules = []Rule{{Enabled: true, Source: "192.0.2.0/24", Destination: "203.0.113.0/24", Protocol: "tcp", DestinationPort: "443", Action: "tun"}, {Enabled: true, Protocol: "udp", DestinationPort: "53", Action: "direct"}}
	return c
}

func TestRejectUnsafeAndAmbiguousPolicy(t *testing.T) {
	for name, change := range map[string]func(*Config){
		"shell interface":        func(c *Config) { c.Ingress = "eth0;reboot" },
		"source injection":       func(c *Config) { c.Rules[0].Source = "0.0.0.0/0\nflush ruleset" },
		"port injection":         func(c *Config) { c.Rules[0].DestinationPort = "443; delete table" },
		"port overflow":          func(c *Config) { c.Rules[0].DestinationPort = "65536" },
		"descending range":       func(c *Config) { c.Rules[0].DestinationPort = "100-1" },
		"ambiguous prefix":       func(c *Config) { c.Rules[0].Source = "192.0.2.5/24" },
		"ipv6":                   func(c *Config) { c.Rules[0].Source = "::/0" },
		"tun loop":               func(c *Config) { c.Rules[0].Interface = c.Tun },
		"missing direct gateway": func(c *Config) { c.Gateway = "" },
		"port without protocol":  func(c *Config) { c.Rules[0].Protocol = "any" },
	} {
		t.Run(name, func(t *testing.T) {
			c := testConfig()
			change(&c)
			if c.Validate() == nil {
				t.Fatal("unsafe policy accepted")
			}
		})
	}
}

type runnerFunc func(context.Context, string, string, ...string) ([]byte, error)

func (f runnerFunc) Run(ctx context.Context, input, name string, args ...string) ([]byte, error) {
	return f(ctx, input, name, args...)
}

func TestNoChangesWithoutPrivilegesOrOnResourceConflict(t *testing.T) {
	for _, conflict := range []bool{false, true} {
		m := NewManager()
		m.ready = func() error {
			if !conflict {
				return errors.New("CAP_NET_ADMIN is unavailable")
			}
			return nil
		}
		m.index = func(string) int { return 1 }
		m.forwarding = func() bool { return true }
		m.runner = runnerFunc(func(_ context.Context, _ string, name string, args ...string) ([]byte, error) {
			command := name + " " + strings.Join(args, " ")
			switch command {
			case "ip -N -j -4 rule show":
				return []byte(`[{"priority":100,"table":254,"fwmark":"0x1","fwmask":"0xffffffff"}]`), nil
			case "ip -N -j -4 route show table all":
				return []byte(`[]`), nil
			case "nft -j list tables":
				return []byte(`{"nftables":[]}`), nil
			default:
				t.Fatalf("unexpected mutation: %s", command)
				return nil, nil
			}
		})
		err := m.Update(context.Background(), testConfig(), func() (Config, error) { t.Fatal("configuration read before preflight completed"); return Config{}, nil }, func(Config) error { t.Fatal("configuration saved on failed preflight"); return nil })
		if err == nil {
			t.Fatal("missing privilege/conflict accepted")
		}
	}
}
