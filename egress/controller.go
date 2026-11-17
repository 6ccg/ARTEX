package egress

import (
	"bufio"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

type Controller struct {
	mu        sync.Mutex
	binary    string
	dir       string
	cmd       *exec.Cmd
	ready     chan struct{}
	done      chan error
	closed    bool
	raw       string
	allowIPv6 bool
}

// Open starts the core inside the dedicated gateway container.
func Open(raw string, allowIPv6 bool) (*Controller, error) {
	binary, err := exec.LookPath("sing-box")
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat("/dev/net/tun"); err != nil {
		return nil, fmt.Errorf("transparent proxy requires Docker NET_ADMIN and /dev/net/tun: %w", err)
	}
	if err := installForwardGuard(); err != nil {
		return nil, err
	}
	if err := installChannelRoutes(); err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp("/run", "artex-network-")
	if err != nil {
		return nil, err
	}
	c := &Controller{binary: binary, dir: dir, ready: make(chan struct{}, 1), done: make(chan error, 1)}
	if err := c.Apply(raw, allowIPv6); err != nil {
		c.Close()
		return nil, err
	}
	return c, nil
}

func (c *Controller) Apply(raw string, allowIPv6 bool) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return fmt.Errorf("transparent proxy has stopped")
	}
	if c.cmd != nil && c.raw == raw && c.allowIPv6 == allowIPv6 {
		return nil
	}
	networks, err := localNetworks()
	if err != nil {
		return err
	}
	config, err := Config(raw, networks, allowIPv6)
	if err != nil {
		return err
	}
	next := filepath.Join(c.dir, "next.json")
	if err := os.WriteFile(next, config, 0o600); err != nil {
		return err
	}
	if output, err := exec.Command(c.binary, "check", "--disable-color", "-c", next).CombinedOutput(); err != nil {
		return fmt.Errorf("transparent proxy config: %w: %s", err, strings.TrimSpace(string(output)))
	}
	path := filepath.Join(c.dir, "config.json")
	if err := os.Rename(next, path); err != nil {
		return err
	}
	if c.cmd == nil {
		cmd := exec.Command(c.binary, "run", "--disable-color", "-c", path)
		setParentDeathSignal(cmd)
		output, err := cmd.StdoutPipe()
		if err != nil {
			return err
		}
		cmd.Stderr = cmd.Stdout
		if err := cmd.Start(); err != nil {
			return err
		}
		c.cmd = cmd
		go func() {
			scanner := bufio.NewScanner(output)
			for scanner.Scan() {
				line := scanner.Text()
				if strings.Contains(line, "sing-box started") {
					select {
					case c.ready <- struct{}{}:
					default:
					}
					log.Printf("[network] container transparent routing ready")
				} else if strings.Contains(line, "ERROR") || strings.Contains(line, "WARN") || strings.Contains(line, "FATAL") {
					log.Printf("[network] %s", line)
				}
			}
		}()
		go func() {
			err := cmd.Wait()
			c.done <- err
			close(c.done)
			c.mu.Lock()
			closed := c.closed
			c.mu.Unlock()
			if !closed {
				log.Fatalf("[network] transparent routing stopped: %v", err)
			}
		}()
	} else {
		select {
		case <-c.ready:
		default:
		}
		if err := c.cmd.Process.Signal(syscall.SIGHUP); err != nil {
			return err
		}
	}
	select {
	case <-c.ready:
		if err := installIngressRoutes(); err != nil {
			return err
		}
		c.raw = raw
		c.allowIPv6 = allowIPv6
		return nil
	case err := <-c.done:
		return fmt.Errorf("transparent proxy failed to start: %v", err)
	case <-time.After(10 * time.Second):
		return fmt.Errorf("transparent proxy did not become ready")
	}
}

func (c *Controller) Close() {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.closed = true
	cmd := c.cmd
	c.mu.Unlock()
	if cmd != nil {
		_ = cmd.Process.Signal(syscall.SIGTERM)
		select {
		case <-c.done:
		case <-time.After(5 * time.Second):
			_ = cmd.Process.Kill()
			<-c.done
		}
	}
	_ = os.RemoveAll(c.dir)
}
