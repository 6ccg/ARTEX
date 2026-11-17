package egress

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

func installChannelRoutes() error {
	for _, family := range []string{"-4", "-6"} {
		_ = exec.Command("ip", family, "rule", "del", "pref", "8998").Run()
		if output, err := exec.Command("ip", family, "rule", "add", "pref", "8998", "fwmark", "0x8791", "lookup", "main").CombinedOutput(); err != nil {
			return fmt.Errorf("configure independent AI/search routes: %w: %s", err, output)
		}
	}
	return nil
}

func installIngressRoutes() error {
	interfaces, err := net.Interfaces()
	if err != nil {
		return err
	}
	for _, iface := range interfaces {
		addresses, _ := iface.Addrs()
		for _, address := range addresses {
			if address.String() != "172.30.254.2/24" {
				continue
			}
			for _, family := range []string{"-4", "-6"} {
				_ = exec.Command("ip", family, "rule", "del", "pref", "8999").Run()
				if output, err := exec.Command("ip", family, "rule", "add", "pref", "8999", "iif", iface.Name, "lookup", "2022").CombinedOutput(); err != nil {
					return fmt.Errorf("route app ingress through TUN: %w: %s", err, output)
				}
			}
			return nil
		}
	}
	return fmt.Errorf("isolated app interface is missing")
}

func installForwardGuard() error {
	paths := []string{"/proc/sys/net/ipv4/ip_forward", "/proc/sys/net/ipv6/conf/all/forwarding"}
	for _, path := range paths {
		if err := os.WriteFile(path, []byte("0\n"), 0o644); err != nil {
			return err
		}
	}
	_ = exec.Command("nft", "delete", "table", "inet", "artex_egress").Run()
	rules := `table inet artex_egress {
  chain forward {
    type filter hook forward priority 0; policy drop;
    iifname "artex-tun" accept
    oifname "artex-tun" accept
  }
  chain control_guard {
    type filter hook output priority 0;
    meta mark 0x8791 accept
    # Runtime data channels may never use a connected-network/loopback shortcut.
    meta mark 0x8793 oifname != "artex-tun" reject
    oifname != "artex-tun" tcp dport { 8790, 8787, 5432 } reject with tcp reset
  }
}
`
	cmd := exec.Command("nft", "-f", "-")
	cmd.Stdin = strings.NewReader(rules)
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("install mandatory proxy forwarding: %w: %s", err, strings.TrimSpace(string(output)))
	}
	for _, path := range paths {
		if err := os.WriteFile(path, []byte("1\n"), 0o644); err != nil {
			return err
		}
	}
	return nil
}

func Serve(ctx context.Context) error {
	var settings ProxySettings
	if stored, err := os.ReadFile("/run/artex-proxies.json"); err == nil {
		if err := json.Unmarshal(stored, &settings); err != nil {
			return err
		}
	}
	if err := SetLLMProxy(settings.LLM); err != nil {
		return err
	}
	if err := SetSearchProxy(settings.Search); err != nil {
		return err
	}
	core, err := Open(settings.Execution, settings.AllowIPv6)
	if err != nil {
		return err
	}
	defer core.Close()
	var settingsMu sync.Mutex
	mux := http.NewServeMux()
	modelTransport := channelTransport(&llmProxy)
	defer modelTransport.CloseIdleConnections()
	searchTransport := channelTransport(&searchProxy)
	defer searchTransport.CloseIdleConnections()
	mux.Handle("/llm", llmHandler(modelTransport))
	mux.Handle("/independent", independentHandler(false))
	mux.Handle("/probe", independentHandler(true))
	mux.HandleFunc("POST /search/probe", searchProbeHandler)
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintln(w, `{"ok":true}`)
	})
	mux.HandleFunc("PUT /proxy", func(w http.ResponseWriter, r *http.Request) {
		var req proxyPatch
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192)).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		settingsMu.Lock()
		defer settingsMu.Unlock()
		next := settings
		for _, field := range []struct {
			input  *string
			output *string
		}{{req.Execution, &next.Execution}, {req.LLM, &next.LLM}, {req.Search, &next.Search}} {
			if field.input != nil {
				*field.output = strings.TrimSpace(*field.input)
				if _, err := ParseProxy(*field.output); err != nil {
					http.Error(w, err.Error(), http.StatusBadRequest)
					return
				}
			}
		}
		if req.AllowIPv6 != nil {
			next.AllowIPv6 = *req.AllowIPv6
		}
		if err := core.Apply(next.Execution, next.AllowIPv6); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		_ = SetLLMProxy(next.LLM)
		_ = SetSearchProxy(next.Search)
		modelTransport.CloseIdleConnections()
		searchTransport.CloseIdleConnections()
		settings = next
		stored, _ := json.Marshal(settings)
		if err := os.WriteFile("/run/artex-proxies.json", stored, 0o600); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	// The public data listener exposes no control routes. Its execution sockets
	// may only leave through the TUN, including when search uses no proxy.
	// Bind control only to the isolated interface, never the default-network,
	// TUN, loopback or IPv6 aliases that Agent traffic could otherwise target.
	server := &http.Server{Addr: "172.30.254.2:8790", Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	data := &http.Server{Addr: ":8791", Handler: searchProxyHandler(searchTransport, &searchProxy, ctx, executionDialer()), ReadHeaderTimeout: 5 * time.Second}
	done := make(chan error, 2)
	go func() { done <- server.ListenAndServe() }()
	go func() { done <- data.ListenAndServe() }()
	defer server.Close()
	defer data.Close()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		stop, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		return server.Shutdown(stop)
	}
}
