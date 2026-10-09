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
	"time"
)

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
	stored, _ := os.ReadFile("/run/artex-global-proxy")
	core, err := Open(strings.TrimSpace(string(stored)))
	if err != nil {
		return err
	}
	defer core.Close()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintln(w, `{"ok":true}`)
	})
	mux.HandleFunc("PUT /proxy", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			URL string `json:"url"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192)).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if err := core.Apply(strings.TrimSpace(req.URL)); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if err := os.WriteFile("/run/artex-global-proxy", []byte(strings.TrimSpace(req.URL)), 0o600); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	server := &http.Server{Addr: ":8790", Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	done := make(chan error, 1)
	go func() { done <- server.ListenAndServe() }()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		stop, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		return server.Shutdown(stop)
	}
}
