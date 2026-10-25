// Command chaos is the demo controller. It mounts the Docker socket, so it
// refuses to start unless DEMO_MODE=true and a demo token is configured.
package main

import (
	"bufio"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/ikshantshukla123/crash-safe-object-store/internal/config"
)

type controller struct {
	cfg    config.ChaosConfig
	log    *slog.Logger
	docker *dockerClient

	k6Mu     sync.Mutex
	k6Status string
	k6Output string
	k6At     time.Time
}

type dockerClient struct {
	http    *http.Client
	host    string
	apiBase string
}

func main() {
	if err := run(); err != nil {
		slog.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.LoadChaos()
	if err != nil {
		return err
	}
	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.LogLevel}))
	slog.SetDefault(log)
	docker, err := newDockerClient(cfg.DockerHost)
	if err != nil {
		return err
	}
	c := &controller{cfg: cfg, log: log, docker: docker, k6Status: "idle"}

	r := chi.NewRouter()
	r.Use(c.auth)
	r.Get("/health", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	r.Post("/chaos/kill/{node}", c.handleKillNode)
	r.Post("/chaos/heal/{node}", c.handleHealNode)
	r.Post("/chaos/kill-server", c.handleKillServer)
	r.Post("/chaos/k6", c.handleK6)
	r.Get("/chaos/k6", c.handleK6Status)
	r.Get("/chaos/logs", c.handleLogs)
	r.Get("/chaos/status", c.handleStatus)

	srv := &http.Server{Addr: cfg.Addr, Handler: r, ReadHeaderTimeout: 5 * time.Second}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	errCh := make(chan error, 1)
	go func() {
		log.Info("chaos controller listening", "addr", cfg.Addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()
	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return srv.Shutdown(shutdownCtx)
}

func (c *controller) auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := r.Header.Get("X-Demo-Token")
		if token == "" {
			token = r.URL.Query().Get("token")
		}
		if !constantTimeEqual(token, c.cfg.DemoToken) {
			writeError(w, http.StatusUnauthorized, "invalid demo token")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (c *controller) handleKillNode(w http.ResponseWriter, r *http.Request) {
	name := c.nodeContainer(chi.URLParam(r, "node"))
	if err := c.docker.kill(r.Context(), name); err != nil {
		c.log.Error("kill node", "container", name, "err", err)
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	c.log.Info("node killed", "container", name, "op_id", "chaos")
	writeJSON(w, http.StatusOK, map[string]string{"status": "killed", "container": name})
}

func (c *controller) handleHealNode(w http.ResponseWriter, r *http.Request) {
	name := c.nodeContainer(chi.URLParam(r, "node"))
	if err := c.docker.start(r.Context(), name); err != nil {
		c.log.Error("heal node", "container", name, "err", err)
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	c.log.Info("node healed", "container", name, "op_id", "chaos")
	writeJSON(w, http.StatusOK, map[string]string{"status": "started", "container": name})
}

func (c *controller) handleKillServer(w http.ResponseWriter, r *http.Request) {
	if err := c.docker.kill(r.Context(), c.cfg.APIBaseURLContainer()); err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	c.log.Info("api killed", "container", c.cfg.APIBaseURLContainer())
	go func() {
		time.Sleep(1500 * time.Millisecond)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if running, _ := c.docker.running(ctx, c.cfg.APIBaseURLContainer()); !running {
			if err := c.docker.start(ctx, c.cfg.APIBaseURLContainer()); err != nil {
				c.log.Error("restart api", "err", err)
			}
		}
	}()
	writeJSON(w, http.StatusOK, map[string]string{"status": "killed", "container": c.cfg.APIBaseURLContainer()})
}

func (c *controller) handleK6(w http.ResponseWriter, r *http.Request) {
	c.k6Mu.Lock()
	if c.k6Status == "running" {
		c.k6Mu.Unlock()
		writeError(w, http.StatusConflict, "k6 is already running")
		return
	}
	c.k6Status = "running"
	c.k6Output = ""
	c.k6At = time.Now()
	c.k6Mu.Unlock()

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		cmd := exec.CommandContext(ctx, "k6", "run", "--quiet", "--summary-export", "/tmp/k6-summary.json", c.cfg.K6Script)
		cmd.Env = append(os.Environ(),
			"BASE_URL="+c.cfg.K6BaseURL,
			"K6_NO_USAGE_REPORT=true",
		)
		out, err := cmd.CombinedOutput()
		c.k6Mu.Lock()
		c.k6Output = string(out)
		c.k6At = time.Now()
		if err != nil {
			c.k6Status = "failed: " + err.Error()
		} else {
			c.k6Status = "complete"
		}
		c.k6Mu.Unlock()
	}()
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "running"})
}

func (c *controller) handleK6Status(w http.ResponseWriter, _ *http.Request) {
	c.k6Mu.Lock()
	defer c.k6Mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{
		"status": c.k6Status, "output": c.k6Output, "at": c.k6At,
	})
}

func (c *controller) handleStatus(w http.ResponseWriter, r *http.Request) {
	out := map[string]any{}
	for _, name := range append([]string{c.cfg.APIBaseURLContainer()}, c.allNodes()...) {
		running, err := c.docker.running(r.Context(), name)
		out[name] = map[string]any{"running": running, "error": errString(err)}
	}
	c.k6Mu.Lock()
	out["k6"] = map[string]any{"status": c.k6Status, "at": c.k6At}
	c.k6Mu.Unlock()
	writeJSON(w, http.StatusOK, out)
}

func (c *controller) handleLogs(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	names := append([]string{c.cfg.APIBaseURLContainer()}, c.allNodes()...)
	lines := make(chan string, 128)
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	for _, name := range names {
		go c.streamLogs(ctx, name, lines)
	}
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if _, err := fmt.Fprint(w, ": ping\n\n"); err != nil {
				return
			}
			flusher.Flush()
		case line := <-lines:
			raw, _ := json.Marshal(line)
			if _, err := fmt.Fprintf(w, "data: %s\n\n", raw); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

func (c *controller) streamLogs(ctx context.Context, container string, out chan<- string) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		c.docker.apiBase+"/containers/"+container+"/logs?follow=1&stdout=1&stderr=1&timestamps=1&tail=25", nil)
	if err != nil {
		return
	}
	resp, err := c.docker.http.Do(req)
	if err != nil {
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return
	}
	reader := bufio.NewReader(resp.Body)
	for {
		line, err := readDockerFrame(reader)
		if err != nil {
			return
		}
		line = strings.TrimRight(line, "\n")
		if line == "" {
			continue
		}
		select {
		case out <- "[" + container + "] " + line:
		case <-ctx.Done():
			return
		}
	}
}

func readDockerFrame(r *bufio.Reader) (string, error) {
	header := make([]byte, 8)
	if _, err := io.ReadFull(r, header); err != nil {
		return "", err
	}
	size := binary.BigEndian.Uint32(header[4:])
	body := make([]byte, size)
	if _, err := io.ReadFull(r, body); err != nil {
		return "", err
	}
	return string(body), nil
}

func (c *controller) nodeContainer(node string) string {
	node = strings.TrimSpace(node)
	if !strings.HasPrefix(node, "node") {
		node = "node" + node
	}
	return c.cfg.NodeContainerPrefix() + strings.TrimPrefix(node, "node")
}

func (c *controller) allNodes() []string {
	return []string{
		c.cfg.NodeContainerPrefix() + "1",
		c.cfg.NodeContainerPrefix() + "2",
		c.cfg.NodeContainerPrefix() + "3",
	}
}

func newDockerClient(host string) (*dockerClient, error) {
	host = strings.TrimPrefix(host, "unix://")
	if host == "" {
		host = "/var/run/docker.sock"
	}
	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", host)
		},
	}
	return &dockerClient{
		http:    &http.Client{Transport: transport},
		host:    host,
		apiBase: "http://docker/v1.45",
	}, nil
}

func (d *dockerClient) kill(ctx context.Context, container string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		d.apiBase+"/containers/"+container+"/kill?signal=SIGKILL", nil)
	if err != nil {
		return err
	}
	return d.do(req, http.StatusNoContent)
}

func (d *dockerClient) start(ctx context.Context, container string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		d.apiBase+"/containers/"+container+"/start", nil)
	if err != nil {
		return err
	}
	return d.do(req, http.StatusNoContent, http.StatusNotModified)
}

func (d *dockerClient) running(ctx context.Context, container string) (bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		d.apiBase+"/containers/"+container+"/json", nil)
	if err != nil {
		return false, err
	}
	resp, err := d.http.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return false, nil
	}
	if resp.StatusCode != http.StatusOK {
		return false, fmt.Errorf("inspect %s: status %d", container, resp.StatusCode)
	}
	var body struct {
		State struct {
			Running bool `json:"Running"`
		} `json:"State"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return false, err
	}
	return body.State.Running, nil
}

func (d *dockerClient) do(req *http.Request, want ...int) error {
	resp, err := d.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	for _, code := range want {
		if resp.StatusCode == code {
			return nil
		}
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	return fmt.Errorf("docker %s: status %d: %s", req.URL.Path, resp.StatusCode, strings.TrimSpace(string(body)))
}

func constantTimeEqual(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	var diff byte
	for i := range a {
		diff |= a[i] ^ b[i]
	}
	return diff == 0
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
