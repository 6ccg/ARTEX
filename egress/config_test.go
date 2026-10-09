package egress

import (
	"encoding/json"
	"testing"
)

func TestUnconfiguredGatewayRejectsExternalTraffic(t *testing.T) {
	data, err := Config("", []string{"172.30.254.0/24"})
	if err != nil {
		t.Fatal(err)
	}
	var config map[string]any
	if err := json.Unmarshal(data, &config); err != nil {
		t.Fatal(err)
	}
	dns := config["dns"].(map[string]any)
	if len(dns["servers"].([]any)) != 1 || dns["servers"].([]any)[0].(map[string]any)["server"] != "127.0.0.11" {
		t.Fatal("unconfigured gateway has external DNS")
	}
	rules := config["route"].(map[string]any)["rules"].([]any)
	if len(rules) != 2 || rules[1].(map[string]any)["action"] != "reject" {
		t.Fatal("unconfigured gateway allows routing")
	}
}

func TestGlobalProxyPreservesAuthenticationAndRoutesDNS(t *testing.T) {
	data, err := Config("socks5://user:p%40ss@192.0.2.1:1081", []string{"172.30.254.0/24"})
	if err != nil {
		t.Fatal(err)
	}
	var config map[string]any
	if err := json.Unmarshal(data, &config); err != nil {
		t.Fatal(err)
	}
	out := config["outbounds"].([]any)[1].(map[string]any)
	if out["type"] != "socks" || out["username"] != "user" || out["password"] != "p@ss" || out["server_port"] != float64(1081) {
		t.Fatalf("proxy options lost: %+v", out)
	}
	dns := config["dns"].(map[string]any)["servers"].([]any)[0].(map[string]any)
	if dns["detour"] != "global" || config["route"].(map[string]any)["final"] != "global" {
		t.Fatal("DNS or traffic bypasses the global proxy")
	}
	excluded := config["inbounds"].([]any)[0].(map[string]any)["route_exclude_address"].([]any)
	for _, prefix := range excluded {
		if prefix == "192.0.2.1/32" || prefix == "10.0.0.0/8" {
			t.Fatal("target addresses are exempted from mandatory routing")
		}
	}
}

func TestProxyURLValidation(t *testing.T) {
	for _, raw := range []string{"http://proxy.example", "https://user:pass@proxy.example", "socks5://[::1]:1080"} {
		if _, err := Config(raw, nil); err != nil {
			t.Fatalf("%s: %v", raw, err)
		}
	}
	for _, raw := range []string{"direct://host", "socks5://host:0", "http://host:99999", "http://"} {
		if _, err := Config(raw, nil); err == nil {
			t.Fatalf("invalid proxy accepted: %s", raw)
		}
	}
}
