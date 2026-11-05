// Package policyrouting manages an isolated IPv4 policy for packets entering
// the container. It never changes main/default routes or the host RouterOS.
package policyrouting

import (
	"fmt"
	"net/netip"
	"regexp"
	"strconv"
	"strings"
)

const (
	TableName      = "xui_policy"
	Owner          = "3x-ui policy routing v1"
	TunTable       = 10001
	DirectTable    = 10002
	TunPriority    = 100
	DirectPriority = 101
	RouteProtocol  = 242
	MarkMask       = "0xffff0000"
	TunMark        = "0x3e010000"
	DirectMark     = "0x3e020000"
)

type Config struct {
	Enabled bool   `json:"enabled"`
	Ingress string `json:"ingress"`
	Tun     string `json:"tun"`
	Uplink  string `json:"uplink"`
	Gateway string `json:"gateway"`
	Rules   []Rule `json:"rules"`
}

type Rule struct {
	Enabled         bool   `json:"enabled"`
	Name            string `json:"name"`
	Source          string `json:"source"`
	Destination     string `json:"destination"`
	Interface       string `json:"interface"`
	Protocol        string `json:"protocol"`
	SourcePort      string `json:"sourcePort"`
	DestinationPort string `json:"destinationPort"`
	Action          string `json:"action"`
}

func DefaultConfig() Config {
	return Config{Ingress: "eth0", Tun: "xray0", Uplink: "eth0", Rules: []Rule{}}
}

var interfaceName = regexp.MustCompile(`^[a-zA-Z0-9_.:-]{1,15}$`)

func validPrefix(value string) bool {
	if value == "" {
		return true
	}
	if ip, err := netip.ParseAddr(value); err == nil {
		return ip.Is4()
	}
	p, err := netip.ParsePrefix(value)
	return err == nil && p.Addr().Is4() && p == p.Masked()
}

func validPort(value string) bool {
	if value == "" {
		return true
	}
	parts := strings.Split(value, "-")
	if len(parts) > 2 {
		return false
	}
	prev := 0
	for _, part := range parts {
		n, err := strconv.Atoi(part)
		if err != nil || n < 1 || n > 65535 || strconv.Itoa(n) != part || n < prev {
			return false
		}
		prev = n
	}
	return true
}

func (c Config) Validate() error {
	if !interfaceName.MatchString(c.Ingress) || !interfaceName.MatchString(c.Tun) || !interfaceName.MatchString(c.Uplink) {
		return fmt.Errorf("interface names must contain 1–15 letters, digits, _, ., :, or -")
	}
	if c.Ingress == c.Tun || c.Uplink == c.Tun {
		return fmt.Errorf("TUN must differ from ingress and uplink")
	}
	if c.Gateway != "" {
		ip, err := netip.ParseAddr(c.Gateway)
		if err != nil || !ip.Is4() || ip.IsUnspecified() || ip.IsMulticast() {
			return fmt.Errorf("gateway must be an IPv4 unicast address")
		}
	}
	if len(c.Rules) > 128 {
		return fmt.Errorf("at most 128 policy rules are supported")
	}
	for i, r := range c.Rules {
		fail := func(reason string) error { return fmt.Errorf("rule %d: %s", i+1, reason) }
		if len(r.Name) > 256 {
			return fail("name is too long")
		}
		if !validPrefix(r.Source) || !validPrefix(r.Destination) {
			return fail("use IPv4 addresses or canonical CIDR prefixes")
		}
		if r.Interface != "" && (!interfaceName.MatchString(r.Interface) || r.Interface == c.Tun) {
			return fail("invalid ingress interface or TUN loop")
		}
		if r.Protocol != "" && r.Protocol != "any" && r.Protocol != "tcp" && r.Protocol != "udp" && r.Protocol != "icmp" {
			return fail("protocol must be any, tcp, udp, or icmp")
		}
		if !validPort(r.SourcePort) || !validPort(r.DestinationPort) {
			return fail("ports must be 1–65535 or an ascending range")
		}
		if (r.SourcePort != "" || r.DestinationPort != "") && r.Protocol != "tcp" && r.Protocol != "udp" {
			return fail("port matching requires tcp or udp")
		}
		if r.Action != "tun" && r.Action != "direct" && r.Action != "bypass" {
			return fail("action must be tun, direct, or bypass")
		}
		if c.Enabled && r.Enabled && r.Action == "direct" && c.Gateway == "" {
			return fail("direct routing requires an uplink gateway")
		}
	}
	return nil
}

// Rules are first-match and only run on ingress. No OUTPUT hook is installed:
// the proxy's own connections must not be marked back into its TUN.
func (c Config) NFTScript(exists bool) string {
	var b strings.Builder
	if exists {
		fmt.Fprintf(&b, "delete table inet %s\n", TableName)
	}
	if !c.Enabled {
		return b.String()
	}
	fmt.Fprintf(&b, "table inet %s {\n comment %s\n chain ingress {\n type filter hook prerouting priority mangle; policy accept;\n", TableName, strconv.Quote(Owner))
	for i, r := range c.Rules {
		if !r.Enabled {
			continue
		}
		iface := r.Interface
		if iface == "" {
			iface = c.Ingress
		}
		fmt.Fprintf(&b, " iifname %s meta nfproto ipv4", strconv.Quote(iface))
		if r.Source != "" {
			fmt.Fprintf(&b, " ip saddr %s", r.Source)
		}
		if r.Destination != "" {
			fmt.Fprintf(&b, " ip daddr %s", r.Destination)
		}
		if r.Protocol != "" && r.Protocol != "any" {
			fmt.Fprintf(&b, " ip protocol %s", r.Protocol)
		}
		if r.SourcePort != "" {
			fmt.Fprintf(&b, " %s sport %s", r.Protocol, r.SourcePort)
		}
		if r.DestinationPort != "" {
			fmt.Fprintf(&b, " %s dport %s", r.Protocol, r.DestinationPort)
		}
		b.WriteString(" counter")
		if r.Action != "bypass" {
			mark := TunMark
			if r.Action == "direct" {
				mark = DirectMark
			}
			fmt.Fprintf(&b, " meta mark set (meta mark & 0x0000ffff) | %s", mark)
		}
		fmt.Fprintf(&b, " return comment %s\n", strconv.Quote("xui-rule:"+strconv.Itoa(i)))
	}
	b.WriteString(" }\n}\n")
	return b.String()
}
