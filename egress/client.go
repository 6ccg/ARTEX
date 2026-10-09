package egress

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
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
	return &Client{base: base, http: &http.Client{Transport: &http.Transport{Proxy: nil}, Timeout: 15 * time.Second}}
}

func (c *Client) Apply(raw string) error {
	body, _ := json.Marshal(map[string]string{"url": raw})
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
	client := http.Client{Transport: &http.Transport{Proxy: nil}, Timeout: 2 * time.Second}
	resp, err := client.Get("http://127.0.0.1:8790/health")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("gateway health status %d", resp.StatusCode)
	}
	return nil
}
