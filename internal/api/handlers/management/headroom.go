package management

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"
)

// managedHeadroom tracks the managed Headroom proxy process.
type managedHeadroom struct {
	pid int
}

var (
	headroomMu      sync.Mutex
	managedProcess  *managedHeadroom
	headroomPIDPath = filepath.Join("headroom", "proxy.pid")
	headroomLogPath = filepath.Join("headroom", "proxy.log")
)

func readHeadroomPID() (int, bool) {
	data, err := os.ReadFile(headroomPIDPath)
	if err != nil {
		return 0, false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid <= 0 {
		return 0, false
	}
	return pid, true
}

func writeHeadroomPID(pid int) error {
	if err := os.MkdirAll(filepath.Dir(headroomPIDPath), 0o700); err != nil {
		return err
	}
	return os.WriteFile(headroomPIDPath, []byte(strconv.Itoa(pid)), 0o600)
}

func clearHeadroomPID() {
	_ = os.Remove(headroomPIDPath)
}

func headroomInstalled() bool {
	path, err := exec.LookPath("headroom")
	if err != nil || path == "" {
		return false
	}
	return true
}

func headroomHealthy(url string) bool {
	client := &http.Client{Timeout: 1 * time.Second}
	resp, err := client.Get(url + "/health")
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode >= 200 && resp.StatusCode < 300
}

func waitForHeadroomHealthy(url string, timeout, interval time.Duration, healthy func(string) bool) bool {
	deadline := time.Now().Add(timeout)
	for {
		if healthy(url) {
			return true
		}
		if time.Now().Add(interval).After(deadline) {
			return false
		}
		time.Sleep(interval)
	}
}

func stopHeadroomPID(pid int) {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return
	}
	_ = proc.Signal(syscall.SIGTERM)
	for i := 0; i < 30; i++ {
		time.Sleep(100 * time.Millisecond)
		if !headroomProcessRunning(pid) {
			return
		}
	}
	_ = proc.Kill()
}

func headroomProcessRunning(pid int) bool {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return proc.Signal(syscall.Signal(0)) == nil
}

func headroomExtractPort(url string) int {
	if url == "" {
		return 8787
	}
	parts := strings.Split(url, ":")
	if len(parts) < 2 {
		return 8787
	}
	portStr := strings.TrimRight(parts[len(parts)-1], "/")
	if port, err := strconv.Atoi(portStr); err == nil && port > 0 {
		return port
	}
	return 8787
}

// HeadroomStatus returns the current Headroom installation and process status.
func (h *Handler) HeadroomStatus(c *gin.Context) {
	h.mu.Lock()
	cfg := h.cfg
	h.mu.Unlock()

	url := "http://127.0.0.1:8787"
	if cfg.TokenSaver.Headroom.URL != "" {
		url = cfg.TokenSaver.Headroom.URL
	}

	var installed, healthy bool
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		installed = headroomInstalled()
	}()
	go func() {
		defer wg.Done()
		healthy = headroomHealthy(url)
	}()
	wg.Wait()

	pid, hasPID := readHeadroomPID()
	managed := hasPID && pid > 0 && headroomProcessRunning(pid)

	c.JSON(http.StatusOK, gin.H{
		"installed": installed,
		"running":   healthy,
		"healthy":   healthy,
		"managed":   managed,
		"url":       url,
	})
}

// HeadroomInstall installs the Headroom CLI. Tries pipx first, then pip variants.
func (h *Handler) HeadroomInstall(c *gin.Context) {
	var body struct {
		Extras []string `json:"extras"`
	}
	_ = c.ShouldBindJSON(&body)

	spec := "headroom-ai[proxy]"
	if len(body.Extras) > 0 {
		spec = fmt.Sprintf("headroom-ai[proxy,%s]", strings.Join(body.Extras, ","))
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Minute)
	defer cancel()

	// Strategy 1: pipx (best for CLI tools — isolated venv, binary on PATH)
	if _, err := exec.LookPath("pipx"); err == nil {
		cmd := exec.CommandContext(ctx, "pipx", "install", "--force", spec)
		setHideWindow(cmd)
		if output, err := cmd.CombinedOutput(); err == nil {
			_ = output
			c.JSON(http.StatusOK, gin.H{"success": true, "installed": headroomInstalled(), "method": "pipx"})
			return
		}
	}

	// Strategy 2: pip --break-system-packages (system-wide on PEP 668 systems)
	cmd2 := exec.CommandContext(ctx, "pip", "install", "--break-system-packages", "--upgrade", spec)
	setHideWindow(cmd2)
	if _, err := cmd2.CombinedOutput(); err == nil {
		c.JSON(http.StatusOK, gin.H{"success": true, "installed": headroomInstalled(), "method": "pip-system"})
		return
	}

	// Strategy 3: pip --user (installs to ~/.local/bin, usually on PATH)
	cmd3 := exec.CommandContext(ctx, "pip", "install", "--user", "--upgrade", spec)
	setHideWindow(cmd3)
	output3, err3 := cmd3.CombinedOutput()
	if err3 == nil {
		c.JSON(http.StatusOK, gin.H{"success": true, "installed": headroomInstalled(), "method": "pip-user"})
		return
	}

	c.JSON(http.StatusInternalServerError, gin.H{
		"success": false,
		"error":   fmt.Sprintf("All install methods failed. Last error: %v\n%s", err3, string(output3)),
		"hint":    "Install pipx (pip install pipx) or create a venv first, then retry.",
	})
}

// HeadroomStart starts the Headroom proxy process.
func (h *Handler) HeadroomStart(c *gin.Context) {
	h.mu.Lock()
	cfg := h.cfg
	h.mu.Unlock()

	url := "http://127.0.0.1:8787"
	if cfg.TokenSaver.Headroom.URL != "" {
		url = cfg.TokenSaver.Headroom.URL
	}
	port := headroomExtractPort(url)

	if headroomHealthy(url) {
		pid, hasPID := readHeadroomPID()
		managed := hasPID && pid > 0 && headroomProcessRunning(pid)
		response := gin.H{"success": true, "managed": managed}
		if managed {
			response["pid"] = pid
		}
		c.JSON(http.StatusOK, response)
		return
	}

	if !headroomInstalled() {
		c.JSON(http.StatusBadRequest, gin.H{
			"success": false,
			"error":   "Headroom CLI not installed.",
		})
		return
	}

	if pid, ok := readHeadroomPID(); ok && headroomProcessRunning(pid) {
		stopHeadroomPID(pid)
	}
	clearHeadroomPID()

	args := []string{"proxy", "--port", strconv.Itoa(port)}

	// Pass compression flags matching 9Router's extrasProxyArgs() behavior.
	if cfg.TokenSaver.Headroom.CodeAware {
		args = append(args, "--code-aware")
	}
	// Kompress defaults to enabled; only pass --disable-kompress when explicitly false.
	if cfg.TokenSaver.Headroom.Kompress != nil && !*cfg.TokenSaver.Headroom.Kompress {
		args = append(args, "--disable-kompress")
	}

	if err := os.MkdirAll(filepath.Dir(headroomLogPath), 0o700); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"success": false,
			"error":   fmt.Sprintf("failed to create log directory: %v", err),
		})
		return
	}
	logFile, err := os.OpenFile(headroomLogPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"success": false,
			"error":   fmt.Sprintf("failed to open log file: %v", err),
		})
		return
	}

	cmd := exec.Command("headroom", args...)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	setHideWindow(cmd)

	if err := cmd.Start(); err != nil {
		logFile.Close()
		c.JSON(http.StatusInternalServerError, gin.H{
			"success": false,
			"error":   fmt.Sprintf("failed to start Headroom: %v", err),
		})
		return
	}

	pid := cmd.Process.Pid
	_ = writeHeadroomPID(pid)

	headroomMu.Lock()
	managedProcess = &managedHeadroom{pid: pid}
	headroomMu.Unlock()

	exitCh := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		logFile.Close()
		headroomMu.Lock()
		if managedProcess != nil && managedProcess.pid == pid {
			managedProcess = nil
		}
		headroomMu.Unlock()
		clearHeadroomPID()
		close(exitCh)
	}()

	const startupTimeout = 8 * time.Second
	select {
	case <-time.After(100 * time.Millisecond):
		if !waitForHeadroomHealthy(url, startupTimeout, 200*time.Millisecond, headroomHealthy) {
			_ = cmd.Process.Kill()
			clearHeadroomPID()
			c.JSON(http.StatusInternalServerError, gin.H{
				"success": false,
				"error":   "Headroom proxy started but did not become healthy — see headroom/proxy.log.",
			})
			return
		}
		c.JSON(http.StatusOK, gin.H{"success": true, "pid": pid})
	case <-exitCh:
		c.JSON(http.StatusInternalServerError, gin.H{
			"success": false,
			"error":   "Headroom proxy exited during startup — see headroom/proxy.log.",
		})
	}
}

// HeadroomStop stops the managed Headroom proxy process.
func (h *Handler) HeadroomStop(c *gin.Context) {
	pid, ok := readHeadroomPID()
	if !ok || pid <= 0 {
		c.JSON(http.StatusOK, gin.H{"success": true, "stopped": false})
		return
	}

	proc, err := os.FindProcess(pid)
	if err != nil {
		clearHeadroomPID()
		c.JSON(http.StatusOK, gin.H{"success": true, "stopped": false})
		return
	}

	if err := proc.Signal(syscall.SIGTERM); err != nil {
		clearHeadroomPID()
		c.JSON(http.StatusOK, gin.H{"success": true, "stopped": false})
		return
	}

	for i := 0; i < 30; i++ {
		time.Sleep(100 * time.Millisecond)
		if !headroomProcessRunning(pid) {
			clearHeadroomPID()
			c.JSON(http.StatusOK, gin.H{"success": true, "stopped": true})
			return
		}
	}

	_ = proc.Kill()
	clearHeadroomPID()
	c.JSON(http.StatusOK, gin.H{"success": true, "stopped": true})
}

// HeadroomRestart stops then starts the Headroom proxy process.
func (h *Handler) HeadroomRestart(c *gin.Context) {
	// Stop existing process
	if pid, ok := readHeadroomPID(); ok && pid > 0 {
		if proc, err := os.FindProcess(pid); err == nil {
			_ = proc.Signal(syscall.SIGTERM)
			for i := 0; i < 30; i++ {
				time.Sleep(100 * time.Millisecond)
				if !headroomProcessRunning(pid) {
					break
				}
			}
			_ = proc.Kill()
		}
		clearHeadroomPID()
	}

	// Start fresh — reuse HeadroomStart logic
	h.HeadroomStart(c)
}
