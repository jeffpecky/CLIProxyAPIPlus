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
	headroomMu       sync.Mutex
	managedProcess   *managedHeadroom
	headroomPIDPath  = filepath.Join("headroom", "proxy.pid")
	headroomLogPath  = filepath.Join("headroom", "proxy.log")
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
	cmd := exec.Command("headroom", "--version")
	cmd.Stdout = nil
	cmd.Stderr = nil
	return cmd.Run() == nil
}

func headroomHealthy(url string) bool {
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get(url + "/health")
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode >= 200 && resp.StatusCode < 300
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

	installed := headroomInstalled()
	healthy := headroomHealthy(url)

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

// HeadroomInstall installs the Headroom CLI. Tries pipx first, falls back to pip.
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

	// Try pipx first (handles PEP 668 / externally-managed-environment)
	if _, err := exec.LookPath("pipx"); err == nil {
		cmd := exec.CommandContext(ctx, "pipx", "install", "--force", spec)
		if output, err := cmd.CombinedOutput(); err == nil {
			_ = output
			c.JSON(http.StatusOK, gin.H{"success": true, "installed": headroomInstalled()})
			return
		}
	}

	// Try pip with --break-system-packages (for PEP 668 systems)
	cmd := exec.CommandContext(ctx, "pip", "install", "--break-system-packages", "--upgrade", spec)
	if _, err := cmd.CombinedOutput(); err != nil {
		// Final fallback: plain pip (may work in venvs)
		cmd2 := exec.CommandContext(ctx, "pip", "install", "--upgrade", spec)
		output2, err2 := cmd2.CombinedOutput()
		if err2 != nil {
			c.JSON(http.StatusInternalServerError, gin.H{
				"success": false,
				"error":   fmt.Sprintf("pip install failed: %v\n%s", err2, string(output2)),
			})
			return
		}
	}

	c.JSON(http.StatusOK, gin.H{"success": true, "installed": headroomInstalled()})
}

// HeadroomStart starts the Headroom proxy process.
func (h *Handler) HeadroomStart(c *gin.Context) {
	if !headroomInstalled() {
		c.JSON(http.StatusBadRequest, gin.H{
			"success": false,
			"error":   "Headroom CLI not installed.",
		})
		return
	}

	h.mu.Lock()
	cfg := h.cfg
	h.mu.Unlock()

	port := headroomExtractPort(cfg.TokenSaver.Headroom.URL)

	// Check if already running
	if pid, ok := readHeadroomPID(); ok && headroomProcessRunning(pid) {
		c.JSON(http.StatusOK, gin.H{"success": true, "pid": pid})
		return
	}
	clearHeadroomPID()

	args := []string{"proxy", "--port", strconv.Itoa(port)}

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
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}

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

	go func() {
		_ = cmd.Wait()
		logFile.Close()
		headroomMu.Lock()
		if managedProcess != nil && managedProcess.pid == pid {
			managedProcess = nil
		}
		headroomMu.Unlock()
		clearHeadroomPID()
	}()

	// Wait for Headroom to become healthy
	url := fmt.Sprintf("http://127.0.0.1:%d", port)
	for i := 0; i < 40; i++ {
		time.Sleep(200 * time.Millisecond)
		if headroomHealthy(url) {
			c.JSON(http.StatusOK, gin.H{"success": true, "pid": pid})
			return
		}
	}

	_ = cmd.Process.Kill()
	clearHeadroomPID()
	c.JSON(http.StatusInternalServerError, gin.H{
		"success": false,
		"error":   "Headroom proxy did not become healthy. See headroom/proxy.log.",
	})
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
