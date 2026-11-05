package policyrouting

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Runner interface {
	Run(context.Context, string, string, ...string) ([]byte, error)
}

type commandRunner struct{}

type cappedBuffer struct{ bytes.Buffer }

func (b *cappedBuffer) Write(p []byte) (int, error) {
	n := len(p)
	if room := (1 << 20) - b.Len(); room > 0 {
		_, _ = b.Buffer.Write(p[:min(room, n)])
	}
	return n, nil
}

func (commandRunner) Run(ctx context.Context, input, name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdin = strings.NewReader(input)
	var out, stderr cappedBuffer
	cmd.Stdout, cmd.Stderr = &out, &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return out.Bytes(), nil
}

type State struct {
	Ready             bool            `json:"ready"`
	Error             string          `json:"error,omitempty"`
	Applied           bool            `json:"applied"`
	WaitingForTun     bool            `json:"waitingForTun"`
	ForwardingEnabled bool            `json:"forwardingEnabled"`
	Interfaces        []string        `json:"interfaces"`
	Rules             json.RawMessage `json:"ipRules,omitempty"`
	Routes            json.RawMessage `json:"ipRoutes,omitempty"`
	NFT               json.RawMessage `json:"nft,omitempty"`
}

type kernelState struct {
	State
	owned  bool
	rules  []map[string]any
	routes []map[string]any
}

type Manager struct {
	mu         sync.Mutex
	runner     Runner
	ready      func() error
	index      func(string) int
	forwarding func() bool
	key        string
	lastError  string
	active     bool
	waiting    bool
}

func NewManager() *Manager {
	return &Manager{runner: commandRunner{}, ready: hostReady, index: interfaceIndex, forwarding: ipv4Forwarding}
}

func ipv4Forwarding() bool {
	data, err := os.ReadFile("/proc/sys/net/ipv4/ip_forward")
	return err == nil && strings.TrimSpace(string(data)) == "1"
}

func (m *Manager) checkConfig(c Config) error {
	if err := c.Validate(); err != nil {
		return err
	}
	if err := m.ready(); err != nil {
		return err
	}
	if c.Enabled && m.index(c.Uplink) == 0 {
		return fmt.Errorf("uplink interface %s is missing or down", c.Uplink)
	}
	if c.Enabled && !m.forwarding() {
		for _, r := range c.Rules {
			if r.Enabled && r.Action != "bypass" {
				return errors.New("net.ipv4.ip_forward is disabled in this container; enable IPv4 forwarding before applying routing rules")
			}
		}
	}
	return nil
}

func interfaceIndex(name string) int {
	iface, err := net.InterfaceByName(name)
	if err != nil || iface.Flags&net.FlagUp == 0 {
		return 0
	}
	return iface.Index
}

func hostReady() error {
	if runtime.GOOS != "linux" {
		return errors.New("native policy routing requires Linux")
	}
	for _, tool := range []string{"ip", "nft"} {
		if _, err := exec.LookPath(tool); err != nil {
			return fmt.Errorf("%s is missing; install iproute2 and nftables", tool)
		}
	}
	status, err := os.ReadFile("/proc/self/status")
	if err != nil {
		return err
	}
	for line := range strings.SplitSeq(string(status), "\n") {
		if value, ok := strings.CutPrefix(line, "CapEff:"); ok {
			caps, err := strconv.ParseUint(strings.TrimSpace(value), 16, 64)
			if err == nil && caps&(1<<12) != 0 {
				return nil
			}
		}
	}
	return errors.New("CAP_NET_ADMIN is unavailable in this container; panel routing cannot be applied")
}

func value(v any) string   { return fmt.Sprint(v) }
func tableID(v any) string { return strings.TrimSuffix(value(v), ".0") }

func unreachableRoute(r map[string]any) bool {
	return value(r["type"]) == "unreachable" || value(r["type"]) == "7"
}

func unicastRoute(r map[string]any) bool {
	t, exists := r["type"]
	return !exists || value(t) == "unicast" || value(t) == "1"
}

func (m *Manager) inspect(ctx context.Context) (kernelState, error) {
	var s kernelState
	var err error
	s.Rules, err = m.runner.Run(ctx, "", "ip", "-N", "-j", "-4", "rule", "show")
	if err != nil {
		return s, err
	}
	s.Routes, err = m.runner.Run(ctx, "", "ip", "-N", "-j", "-4", "route", "show", "table", "all")
	if err != nil {
		return s, err
	}
	if err := json.Unmarshal(s.Rules, &s.rules); err != nil {
		return s, err
	}
	if err := json.Unmarshal(s.Routes, &s.routes); err != nil {
		return s, err
	}
	tables, err := m.runner.Run(ctx, "", "nft", "-j", "list", "tables")
	if err != nil {
		return s, err
	}
	var listed struct {
		NFTables []struct {
			Table struct {
				Family string `json:"family"`
				Name   string `json:"name"`
			} `json:"table"`
		} `json:"nftables"`
	}
	if err := json.Unmarshal(tables, &listed); err != nil {
		return s, err
	}
	for _, entry := range listed.NFTables {
		if entry.Table.Name != TableName || entry.Table.Family != "inet" {
			continue
		}
		s.NFT, err = m.runner.Run(ctx, "", "nft", "-j", "list", "table", "inet", TableName)
		if err != nil {
			return s, err
		}
		var detail struct {
			NFTables []struct {
				Table struct {
					Comment string `json:"comment"`
				} `json:"table"`
			} `json:"nftables"`
		}
		if err := json.Unmarshal(s.NFT, &detail); err != nil {
			return s, err
		}
		for _, item := range detail.NFTables {
			if item.Table.Comment == Owner {
				s.owned = true
			}
		}
		if !s.owned {
			return s, fmt.Errorf("nft table %s already exists and is not owned by 3x-ui", TableName)
		}
	}
	for _, r := range s.routes {
		if tableID(r["table"]) != strconv.Itoa(TunTable) && tableID(r["table"]) != strconv.Itoa(DirectTable) {
			continue
		}
		if !s.owned || value(r["protocol"]) != strconv.Itoa(RouteProtocol) || value(r["dst"]) != "default" || (tableID(r["metric"]) != "10" && tableID(r["metric"]) != "32767") {
			return s, errors.New("reserved route tables 10001/10002 are already in use")
		}
		if (tableID(r["metric"]) == "32767" && !unreachableRoute(r)) || (tableID(r["metric"]) == "10" && !unicastRoute(r)) {
			return s, fmt.Errorf("reserved route table %s has unexpected type %v at metric %v", tableID(r["table"]), r["type"], r["metric"])
		}
	}
	for _, r := range s.rules {
		priority := tableID(r["priority"])
		table := tableID(r["table"])
		if priority != strconv.Itoa(TunPriority) && priority != strconv.Itoa(DirectPriority) && table != strconv.Itoa(TunTable) && table != strconv.Itoa(DirectTable) {
			continue
		}
		mark, wantedTable, wantedPriority := TunMark, strconv.Itoa(TunTable), strconv.Itoa(TunPriority)
		if table == strconv.Itoa(DirectTable) {
			mark, wantedTable, wantedPriority = DirectMark, strconv.Itoa(DirectTable), strconv.Itoa(DirectPriority)
		}
		if !s.owned || priority != wantedPriority || table != wantedTable || value(r["fwmark"]) != mark || value(r["fwmask"]) != MarkMask {
			return s, errors.New("reserved policy rule priorities 100/101 or route tables are already in use")
		}
		if src, ok := r["src"]; ok && src != "all" && src != "0.0.0.0/0" {
			return s, errors.New("reserved policy rule contains a foreign source selector")
		}
		for _, selector := range []string{"dst", "iif", "oif", "uidrange", "ipproto", "sport", "dport", "not", "suppress_prefixlength", "suppress_ifgroup"} {
			if _, exists := r[selector]; exists {
				return s, errors.New("reserved policy rule contains a foreign selector")
			}
		}
	}
	return s, nil
}

func (m *Manager) Status(ctx context.Context) State {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := State{Applied: m.active, WaitingForTun: m.waiting, ForwardingEnabled: m.forwarding(), Interfaces: []string{}}
	ifaces, _ := net.Interfaces()
	for _, iface := range ifaces {
		s.Interfaces = append(s.Interfaces, iface.Name)
	}
	if err := m.ready(); err != nil {
		s.Error = err.Error()
		return s
	}
	s.Ready = true
	kernel, err := m.inspect(ctx)
	// Retain valid read-only diagnostics even when ownership conflicts block
	// application. Never embed a truncated command response as raw JSON.
	if json.Valid(kernel.Rules) {
		s.Rules = kernel.Rules
	}
	if json.Valid(kernel.Routes) {
		s.Routes = kernel.Routes
	}
	if json.Valid(kernel.NFT) {
		s.NFT = kernel.NFT
	}
	if err != nil {
		s.Ready = false
		s.Error = err.Error()
		return s
	}
	s.Error = m.lastError
	return s
}

func (m *Manager) configKey(c Config) string {
	data, _ := json.Marshal(c)
	if !c.Enabled {
		return string(data)
	}
	return string(data) + ":" + strconv.Itoa(m.index(c.Tun)) + ":" + strconv.Itoa(m.index(c.Uplink))
}

// Reconcile runs after startup or TUN interface recreation. A stable policy
// only checks interface indices; no commands or SQLite writes occur per tick.
func (m *Manager) Reconcile(ctx context.Context, c Config) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.key == m.configKey(c) {
		return nil
	}
	err := m.apply(ctx, c)
	if err != nil {
		m.lastError = err.Error()
	}
	return err
}

// Update validates and checks the kernel before saving desired state. Saving
// precedes activation so a process crash can recover the intended policy.
// Failed application restores the old policy and its saved configuration.
func (m *Manager) Update(ctx context.Context, c Config, load func() (Config, error), save func(Config) error) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.checkConfig(c); err != nil {
		return err
	}
	s, err := m.inspect(ctx)
	if err != nil {
		return err
	}
	if script := c.NFTScript(s.owned); script != "" {
		if _, err := m.runner.Run(ctx, script, "nft", "-c", "-f", "-"); err != nil {
			return err
		}
	}
	old, err := load()
	if err != nil {
		return err
	}
	if err := save(c); err != nil {
		return err
	}
	if err := m.apply(ctx, c); err != nil {
		rollbackCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
		defer cancel()
		restoreErr := m.apply(rollbackCtx, old)
		saveErr := save(old)
		m.lastError = errors.Join(err, restoreErr, saveErr).Error()
		return fmt.Errorf("policy application failed (rollback attempted): %w", errors.Join(err, restoreErr, saveErr))
	}
	return nil
}

func (m *Manager) apply(ctx context.Context, c Config) error {
	if err := m.checkConfig(c); err != nil {
		return err
	}
	s, err := m.inspect(ctx)
	if err != nil {
		return err
	}
	run := func(args ...string) error { _, err := m.runner.Run(ctx, "", "ip", args...); return err }
	if !c.Enabled {
		if s.owned {
			// Stop marking first, but keep the ownership lease until all IP
			// objects are removed so a failed cleanup can still roll back.
			clear := fmt.Sprintf("delete table inet %s\ntable inet %s { comment %s; }\n", TableName, TableName, strconv.Quote(Owner))
			if _, err := m.runner.Run(ctx, clear, "nft", "-f", "-"); err != nil {
				return err
			}
		}
		for _, r := range s.rules {
			if tableID(r["priority"]) != strconv.Itoa(TunPriority) && tableID(r["priority"]) != strconv.Itoa(DirectPriority) {
				continue
			}
			if err := run("-4", "rule", "del", "priority", tableID(r["priority"]), "fwmark", value(r["fwmark"])+"/"+MarkMask, "table", tableID(r["table"])); err != nil {
				return err
			}
		}
		for _, r := range s.routes {
			if tableID(r["table"]) != strconv.Itoa(TunTable) && tableID(r["table"]) != strconv.Itoa(DirectTable) {
				continue
			}
			args := []string{"-4", "route", "del"}
			if unreachableRoute(r) {
				args = append(args, "unreachable")
			}
			args = append(args, value(r["dst"]), "table", tableID(r["table"]), "proto", strconv.Itoa(RouteProtocol))
			if metric, ok := r["metric"]; ok {
				args = append(args, "metric", tableID(metric))
			}
			if err := run(args...); err != nil {
				return err
			}
		}
		if s.owned {
			if _, err := m.runner.Run(ctx, c.NFTScript(true), "nft", "-f", "-"); err != nil {
				return err
			}
		}
		m.key, m.active, m.waiting, m.lastError = m.configKey(c), false, false, ""
		return nil
	}
	if m.index(c.Uplink) == 0 {
		return fmt.Errorf("uplink interface %s is missing or down", c.Uplink)
	}
	if !s.owned {
		lease := fmt.Sprintf("table inet %s { comment %s; }\n", TableName, strconv.Quote(Owner))
		if _, err := m.runner.Run(ctx, lease, "nft", "-f", "-"); err != nil {
			return err
		}
	}
	for _, table := range []int{TunTable, DirectTable} {
		if err := run("-4", "route", "replace", "unreachable", "default", "table", strconv.Itoa(table), "metric", "32767", "proto", strconv.Itoa(RouteProtocol)); err != nil {
			return err
		}
	}
	tunIndex := m.index(c.Tun)
	if tunIndex != 0 {
		if err := run("-4", "route", "replace", "default", "dev", c.Tun, "table", strconv.Itoa(TunTable), "metric", "10", "proto", strconv.Itoa(RouteProtocol)); err != nil {
			return err
		}
	}
	if c.Gateway != "" {
		if err := run("-4", "route", "replace", "default", "via", c.Gateway, "dev", c.Uplink, "table", strconv.Itoa(DirectTable), "metric", "10", "proto", strconv.Itoa(RouteProtocol)); err != nil {
			return err
		}
	}
	for _, r := range s.routes {
		table := tableID(r["table"])
		remove := table == strconv.Itoa(TunTable) && tunIndex == 0 || table == strconv.Itoa(DirectTable) && c.Gateway == ""
		if remove && tableID(r["metric"]) == "10" && !unreachableRoute(r) {
			if err := run("-4", "route", "del", "default", "table", table, "metric", "10", "proto", strconv.Itoa(RouteProtocol)); err != nil {
				return err
			}
		}
	}
	for _, spec := range []struct {
		table, priority int
		mark            string
	}{{TunTable, TunPriority, TunMark}, {DirectTable, DirectPriority, DirectMark}} {
		found := false
		for _, r := range s.rules {
			if tableID(r["priority"]) == strconv.Itoa(spec.priority) {
				found = true
			}
		}
		if !found {
			if err := run("-4", "rule", "add", "priority", strconv.Itoa(spec.priority), "fwmark", spec.mark+"/"+MarkMask, "table", strconv.Itoa(spec.table), "protocol", strconv.Itoa(RouteProtocol)); err != nil {
				return err
			}
		}
	}
	if _, err := m.runner.Run(ctx, c.NFTScript(true), "nft", "-f", "-"); err != nil {
		return err
	}
	m.key, m.active, m.waiting, m.lastError = m.configKey(c), true, tunIndex == 0, ""
	return nil
}
