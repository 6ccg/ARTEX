package egress

import (
	"encoding/json"
	"testing"
)

func TestUnconfiguredGatewayUsesDirectTrafficAndDockerDNS(t *testing.T) {
	data, err := Config("", []string{"172.30.254.0/24"})
	if err != nil {
		t.Fatal(err)
	}
	var config map[string]any
	if err := json.Unmarshal(data, &config); err != nil {
		t.Fatal(err)
	}
	dns := config["dns"].(map[string]any)
	if dns["final"] != "docker" || len(dns["servers"].([]any)) != 1 || dns["servers"].([]any)[0].(map[string]any)["server"] != "127.0.0.11" {
		t.Fatal("unconfigured gateway must resolve domains through Docker DNS")
	}
	if _, exists := dns["rules"]; exists {
		t.Fatal("unconfigured gateway must not reject external DNS")
	}
	route := config["route"].(map[string]any)
	rules := route["rules"].([]any)
	if route["final"] != "direct" || len(rules) != 1 || rules[0].(map[string]any)["action"] != "hijack-dns" {
		t.Fatal("unconfigured gateway must allow direct traffic and handle DNS")
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

func TestProxyHostnameResolver(t *testing.T) {
	for _, test := range []struct {
		host     string
		resolver string
	}{
		{"host.docker.internal", "docker"},
		{"gateway.docker.internal", "docker"},
		{"HOST.DOCKER.INTERNAL.", "docker"},
		{"proxy.example", "bootstrap"},
	} {
		t.Run(test.host, func(t *testing.T) {
			data, err := Config("socks5://"+test.host+":1080", nil)
			if err != nil {
				t.Fatal(err)
			}
			var config map[string]any
			if err := json.Unmarshal(data, &config); err != nil {
				t.Fatal(err)
			}
			out := config["outbounds"].([]any)[1].(map[string]any)
			if out["domain_resolver"] != test.resolver {
				t.Fatalf("proxy resolver = %v, want %s", out["domain_resolver"], test.resolver)
			}
		})
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
