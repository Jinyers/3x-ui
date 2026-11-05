package nodee2e

import (
	"encoding/json"
	"net/http"
	"strconv"
	"testing"
)

// The router has no local clients: all adopted client records belong to a
// remote panel, whose accounting remains in its normal mode.
func TestLowWriteMasterManagesNormalNode(t *testing.T) {
	bin := panelBinary(t)
	master := newPanel(t, bin, "low-write-master")
	node := newPanel(t, bin, "normal-node")
	master.extraEnv = []string{"XUI_DISABLE_TRAFFIC_STATS=true", "XUI_DISABLE_METRICS_PERSISTENCE=true"}
	node.extraEnv = []string{"XUI_DISABLE_TRAFFIC_STATS=false", "XUI_DISABLE_METRICS_PERSISTENCE=false"}
	linkToken := node.mintToken("low-write-master-link", "node-sync")
	master.start()
	node.start()
	port := freePort(t)
	node.call(http.MethodPost, "/panel/api/inbounds/add", vlessInbound("existing-on-node", port, nil, vlessClient("remote-client")))
	var enrolled struct {
		Id int `json:"id"`
	}
	obj := master.call(http.MethodPost, "/panel/api/nodes/add", map[string]any{
		"name": "remote", "scheme": "http", "address": "127.0.0.1", "port": node.port, "basePath": "/",
		"apiToken": linkToken, "enable": true, "allowPrivateAddress": true,
	})
	if err := json.Unmarshal(obj, &enrolled); err != nil || enrolled.Id == 0 {
		t.Fatalf("node enrollment failed: %v", err)
	}
	pr := &pair{t: t, master: master, node: node, nodeID: enrolled.Id}
	var adopted inboundView
	eventually(t, settleTimeout, "low-write master adopts remote inbound and client", func() (bool, string) {
		var found bool
		adopted, found = pr.masterInbound(port)
		return found && hasAll(adopted.emails(), "remote-client"), "remote configuration not adopted"
	})
	// Edit the remote inbound through the router's API without a local pool.
	body := vlessInbound("edited-through-router", port, &pr.nodeID)
	body["settings"] = `{"decryption":"none"}`
	master.call(http.MethodPost, "/panel/api/inbounds/update/"+strconv.Itoa(adopted.Id), body)
	pr.waitNode("router edits reach the normal node", port, func(ib inboundView) bool {
		return ib.Remark == "edited-through-router" && hasAll(ib.emails(), "remote-client")
	})
	master.call(http.MethodPost, "/panel/api/clients/add", map[string]any{
		"client": map[string]any{"email": "added-through-router", "enable": true}, "inboundIds": []int{adopted.Id},
	})
	pr.waitNode("router client changes reach the normal node", port, func(ib inboundView) bool {
		return hasAll(ib.emails(), "remote-client", "added-through-router")
	})
	eventually(t, settleTimeout, "router configuration push settles", func() (bool, string) {
		var state struct {
			ConfigDirty bool `json:"configDirty"`
		}
		if err := json.Unmarshal(master.call(http.MethodGet, "/panel/api/nodes/get/"+strconv.Itoa(pr.nodeID), nil), &state); err != nil {
			t.Fatal(err)
		}
		return !state.ConfigDirty, "configuration push is pending"
	})
	secondPort := freePort(t)
	node.call(http.MethodPost, "/panel/api/inbounds/add", vlessInbound("created-remotely", secondPort, nil, vlessClient("second-remote-client")))
	eventually(t, settleTimeout, "later remote configuration is imported", func() (bool, string) {
		ib, ok := pr.masterInbound(secondPort)
		return ok && hasAll(ib.emails(), "second-remote-client"), "later inbound not adopted"
	})
	for _, ib := range master.inbounds() {
		if ib.NodeID == nil {
			t.Fatal("management test unexpectedly created a local inbound")
		}
	}
}
