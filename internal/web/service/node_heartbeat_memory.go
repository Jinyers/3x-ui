package service

import (
	"slices"
	"sync"
	"time"

	"gorm.io/gorm"

	"github.com/mhsanaei/3x-ui/v3/internal/config"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

// The DB identity scopes the cache to one panel database, including tests and
// database replacement. It contains observations only, never node credentials.
var volatileHeartbeats struct {
	sync.RWMutex
	db    *gorm.DB
	nodes map[int]volatileHeartbeat
}

type volatileHeartbeat struct {
	patch HeartbeatPatch
	at    int64
}

func rememberVolatileHeartbeat(db *gorm.DB, id int, p HeartbeatPatch) {
	p.UnavailableMetrics = slices.Clone(p.UnavailableMetrics)
	volatileHeartbeats.Lock()
	defer volatileHeartbeats.Unlock()
	if volatileHeartbeats.db != db {
		volatileHeartbeats.db = db
		volatileHeartbeats.nodes = make(map[int]volatileHeartbeat)
	}
	volatileHeartbeats.nodes[id] = volatileHeartbeat{patch: p, at: time.Now().UnixMilli()}
}

func forgetVolatileHeartbeat(db *gorm.DB, id int) {
	volatileHeartbeats.Lock()
	defer volatileHeartbeats.Unlock()
	if volatileHeartbeats.db == db {
		delete(volatileHeartbeats.nodes, id)
	}
}

func applyVolatileHeartbeat(db *gorm.DB, n *model.Node) {
	if !config.IsMetricsPersistenceDisabled() {
		return
	}
	volatileHeartbeats.RLock()
	v, ok := volatileHeartbeats.nodes[n.Id]
	sameDB := volatileHeartbeats.db == db
	volatileHeartbeats.RUnlock()
	if !sameDB || !ok {
		return
	}
	p := v.patch
	n.LastHeartbeat, n.LatencyMs = p.LastHeartbeat, p.LatencyMs
	n.CpuPct, n.MemPct, n.UptimeSecs = p.CpuPct, p.MemPct, p.UptimeSecs
	n.NetUp, n.NetDown = p.NetUp, p.NetDown
	n.UnavailableMetrics = slices.Clone(p.UnavailableMetrics)
	n.UpdatedAt = max(n.UpdatedAt, v.at)
}

// Keep stable identity and health in SQLite for reconciliation and alerts.
// Only actual changes write; volatile probe timestamps and samples stay in RAM.
func changedHeartbeatState(db *gorm.DB, id int, p HeartbeatPatch) (map[string]any, error) {
	var old model.Node
	if err := db.Select("status", "guid", "xray_version", "panel_version", "last_error", "xray_state", "xray_error").
		Where("id = ?", id).First(&old).Error; err != nil {
		return nil, err
	}
	previous := map[string]string{
		"status": old.Status, "guid": old.Guid,
		"xray_version": old.XrayVersion, "panel_version": old.PanelVersion,
		"last_error": old.LastError, "xray_state": old.XrayState, "xray_error": old.XrayError,
	}
	next := map[string]string{
		"status": p.Status, "guid": p.Guid,
		"xray_version": p.XrayVersion, "panel_version": p.PanelVersion,
		"last_error": p.LastError, "xray_state": p.XrayState, "xray_error": p.XrayError,
	}
	updates := make(map[string]any)
	for column, value := range next {
		if column == "guid" && value == "" {
			continue
		}
		if value != previous[column] {
			updates[column] = value
		}
	}
	return updates, nil
}
