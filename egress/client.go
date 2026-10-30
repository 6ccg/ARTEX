package egress

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"time"
)

type Client struct {
	base string
	http *http.Client
}

func FromEnvironment() *Client {
	base := strings.TrimRight(os.Getenv("ARTEX_EGRESS_URL"), "/")
	if base == "" {
		return nil
	}
	return &Client{base: base, http: &http.Client{Transport: backendTransport(), Timeout: 15 * time.Second}}
}

func (c *Client) Apply(raw string) error {
	return c.apply(proxyPatch{Execution: &raw})
}

func (c *Client) ApplyLLM(raw string) error {
	return c.apply(proxyPatch{LLM: &raw})
}

func (c *Client) ApplySearch(raw string) error {
	return c.apply(proxyPatch{Search: &raw})
}

func (c *Client) ApplyIPv6(allowed bool) error {
	return c.apply(proxyPatch{AllowIPv6: &allowed})
}

func (c *Client) ApplyAll(settings ProxySettings) error {
	return c.apply(proxyPatch{Execution: &settings.Execution, LLM: &settings.LLM, Search: &settings.Search, AllowIPv6: &settings.AllowIPv6})
}

func (c *Client) apply(patch proxyPatch) error {
	body, _ := json.Marshal(patch)
	req, err := http.NewRequest(http.MethodPut, c.base+"/proxy", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("global proxy gateway: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("global proxy gateway: %s", strings.TrimSpace(string(body)))
	}
	return nil
}

func HealthCheck() error {
	client := http.Client{Transport: &http.Transport{DialContext: (&net.Dialer{Control: gatewayHealthControl}).DialContext}, Timeout: 2 * time.Second}
	resp, err := client.Get("http://172.30.254.2:8790/health")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("gateway health status %d", resp.StatusCode)
	}
	return nil
}
