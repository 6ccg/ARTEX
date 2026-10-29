// Package egress manages the Docker container's transparent network exit.
package egress

import (
	"encoding/json"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
)

const (
	InterfaceName = "artex-tun"
	DNSAddress    = "172.30.255.2"
)

func Config(raw string, localNetworks []string) ([]byte, error) {
	dockerDomains := []string{"postgres", "egress", "host.docker.internal", "gateway.docker.internal"}
	final := "direct"
	outbounds := []map[string]any{{"type": "direct", "tag": "direct"}}
	excluded := append([]string{"127.0.0.0/8", "::1/128"}, localNetworks...)
	if raw != "" {
		u, err := url.Parse(raw)
		if err != nil || u.Hostname() == "" {
			return nil, fmt.Errorf("invalid global proxy URL")
		}
		port := 1080
		out := map[string]any{"tag": "global", "server": u.Hostname(), "domain_resolver": "bootstrap"}
		// An explicit outbound resolver bypasses DNS rules, so Docker-only
		// proxy names must use Docker DNS instead of public bootstrap DNS.
		for _, domain := range dockerDomains {
			if strings.EqualFold(strings.TrimSuffix(u.Hostname(), "."), domain) {
				out["domain_resolver"] = "docker"
				break
			}
		}
		switch u.Scheme {
		case "socks5":
			out["type"], out["version"] = "socks", "5"
		case "http", "https":
			out["type"], port = "http", 80
			if u.Scheme == "https" {
				port = 443
				out["tls"] = map[string]any{"enabled": true, "server_name": u.Hostname()}
			}
		default:
			return nil, fmt.Errorf("unsupported global proxy scheme %q", u.Scheme)
		}
		if u.Port() != "" {
			port, err = strconv.Atoi(u.Port())
			if err != nil || port < 1 || port > 65535 {
				return nil, fmt.Errorf("invalid global proxy port")
			}
		}
		out["server_port"] = port
		if u.User != nil {
			out["username"] = u.User.Username()
			out["password"], _ = u.User.Password()
		}
		outbounds = append(outbounds, out)
		final = "global"
	}
	doh := func(tag, detour string) map[string]any {
		server := map[string]any{"type": "https", "tag": tag, "server": "1.1.1.1",
			"tls": map[string]any{"enabled": true, "server_name": "cloudflare-dns.com"}}
		if detour != "direct" {
			server["detour"] = detour
		}
		return server
	}
	config := map[string]any{
		"log": map[string]any{"level": "info", "disabled": false},
		"dns": map[string]any{"servers": []any{doh("remote", final), doh("bootstrap", "direct"), map[string]any{"type": "udp", "tag": "docker", "server": "127.0.0.11", "bind_interface": "lo"}}, "final": "remote", "strategy": "prefer_ipv4",
			"rules": []any{map[string]any{"domain": dockerDomains, "action": "route", "server": "docker"}}},
		"inbounds": []any{map[string]any{
			"type": "tun", "tag": "container", "interface_name": InterfaceName,
			"address":    []string{"172.30.255.1/30", "fdfe:dcba:9876::1/126"},
			"auto_route": true, "auto_redirect": false, "strict_route": true,
			"dns_mode": "disabled", "route_exclude_address": excluded,
		}},
		"outbounds": outbounds,
		"route": map[string]any{"auto_detect_interface": true, "default_domain_resolver": "remote", "final": final,
			"rules": []any{map[string]any{"port": 53, "action": "hijack-dns"}}},
	}
	if raw == "" {
		dns := config["dns"].(map[string]any)
		dns["servers"] = []any{map[string]any{"type": "udp", "tag": "docker", "server": "127.0.0.11", "bind_interface": "lo"}}
		dns["final"] = "docker"
		delete(dns, "rules")
		delete(config["route"].(map[string]any), "default_domain_resolver")
	}
	return json.MarshalIndent(config, "", "  ")
}

func localNetworks() ([]string, error) {
	interfaces, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	var networks []string
	for _, iface := range interfaces {
		if iface.Name == InterfaceName || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addresses, err := iface.Addrs()
		if err != nil {
			return nil, err
		}
		for _, addr := range addresses {
			prefix, err := netip.ParsePrefix(addr.String())
			if err == nil {
				networks = append(networks, prefix.Masked().String())
			}
		}
	}
	return networks, nil
}
