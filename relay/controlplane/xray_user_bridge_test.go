package controlplane

import (
	"encoding/json"
	"testing"
)

func TestBuildUserBridgeConfigsPreservesRemnawaveUser(t *testing.T) {
	routes := FabricRouteInventory{
		SchemaVersion: 2,
		DefaultRoute:  "auto",
		Routes: []FabricRoute{
			{
				ID: "auto", DisplayName: "AUTO", Region: "AUTO", Mode: "direct", Auto: true, Default: true,
				Xray: FabricXrayRoute{Address: "127.0.0.1", Port: 11880, Protocol: "vless", Network: "raw", Security: "none", InboundTag: "FABRIC-VCONNECT-AUTO"},
			},
		},
	}
	identity := FabricUserIdentity{UserID: "user01", VLESSUUID: "11111111-2222-4333-8444-555555555555"}
	xrayConfig, egressConfig, listeners, err := buildUserBridgeConfigs(routes, identity)
	if err != nil {
		t.Fatal(err)
	}
	if len(listeners) != 1 {
		t.Fatalf("listeners=%d, want 1", len(listeners))
	}
	encoded, err := json.Marshal(xrayConfig)
	if err != nil {
		t.Fatal(err)
	}
	if !containsJSONText(encoded, identity.VLESSUUID) {
		t.Fatalf("per-user Xray config does not contain Remnawave vlessUuid")
	}
	encodedEgress, err := json.Marshal(egressConfig)
	if err != nil {
		t.Fatal(err)
	}
	if !containsJSONText(encodedEgress, "auto") || !containsJSONText(encodedEgress, "socks5") {
		t.Fatalf("unexpected generated egress config: %s", encodedEgress)
	}
}

func containsJSONText(data []byte, needle string) bool {
	for i := 0; i+len(needle) <= len(data); i++ {
		if string(data[i:i+len(needle)]) == needle {
			return true
		}
	}
	return false
}
