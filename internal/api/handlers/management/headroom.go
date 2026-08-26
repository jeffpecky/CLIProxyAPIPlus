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
	"time"

	"github.com/gin-gonic/gin"
)

const (
	headroomStartupTimeout = 30 * time.Second
	headroomPollInterval   = 200 * time.Millisecond
)

type headroomStartKind int

const (
	headroomStartSpawn headroomStartKind = iota
	headroomStartReuse
	headroomStartConflict
)

type headroomStartDecision struct {
	kind     headroomStartKind
	managed  bool
	ownerPID int
}

// managedHeadroom tracks the managed Headroom proxy process.
type managedHeadroom struct {
	pid      int
	identity string
}

type headroomOwnership struct {
	pid      int
	identity string
}

var (
	headroomMu          sync.Mutex
	headroomLifecycleMu sync.Mutex
	managedProcess      *managedHeadroom
	headroomPIDPath     = filepath.Join("headroom", "proxy.pid")
	headroomLogPath     = filepath.Join("headroom", "proxy.log")
	stopProcessTree     = stopHeadroomProcessTree
)

func readHeadroomPID() (headroomOwnership, bool) {
	data, err := os.ReadFile(headroomPIDPath)
	if err != nil {
		return headroomOwnership{}, false
	}
	parts := strings.Fields(string(data))
	if len(parts) != 2 {
		return headroomOwnership{}, false
	}
	pid, err := strconv.Atoi(parts[0])
	if err != nil || pid <= 0 || parts[1] == "" {
		return headroomOwnership{}, false
	}
	return headroomOwnership{pid: pid, identity: parts[1]}, true
}

func writeHeadroomPID(pid int, identity string) error {
	if err := os.MkdirAll(filepath.Dir(headroomPIDPath), 0o700); err != nil {
		return err
	}
	return os.WriteFile(headroomPIDPath, []byte(fmt.Sprintf("%d %s", pid, identity)), 0o600)
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
	return waitForHeadroomHealthyWithClock(url, timeout, interval, healthy, time.Now, time.Sleep)
}

func waitForHeadroomHealthyWithClock(
	url string,
	timeout time.Duration,
	interval time.Duration,
	healthy func(string) bool,
	now func() time.Time,
	sleep func(time.Duration),
) bool {
	deadline := now().Add(timeout)
	for {
		if healthy(url) {
			return true
		}
		if now().Add(interval).After(deadline) {
			return false
		}
		sleep(interval)
	}
}

func classifyHeadroomStart(healthy bool, ownerPID int, managed bool) headroomStartDecision {
	if healthy {
		return headroomStartDecision{kind: headroomStartReuse, managed: managed, ownerPID: ownerPID}
	}
	if ownerPID > 0 {
		return headroomStartDecision{kind: headroomStartConflict, ownerPID: ownerPID}
	}
	return headroomStartDecision{kind: headroomStartSpawn}
}

func headroomPortConflictMessage(port, ownerPID int) string {
	if ownerPID > 0 {
		return fmt.Sprintf("Port %d is already occupied by PID %d, but /health is not healthy.", port, ownerPID)
	}
	return fmt.Sprintf("Port %d is already occupied, but /health is not healthy.", port)
}

func headroomOwnershipRunning(ownership headroomOwnership) bool {
	identity, ok := headroomProcessIdentity(ownership.pid)
	return ok && identity == ownership.identity
}

func stopHeadroomPID(ownership headroomOwnership) error {
	if !headroomOwnershipRunning(ownership) {
		return fmt.Errorf("PID %d no longer matches managed Headroom process", ownership.pid)
	}
	if !headroomManagedProcessRunning(ownership.pid) {
		return fmt.Errorf("PID %d is not a Headroom process", ownership.pid)
	}
	return stopProcessTree(ownership.pid)
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

	ownership, hasPID := readHeadroomPID()
	managed := hasPID && headroomOwnershipRunning(ownership)

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
	headroomLifecycleMu.Lock()
	defer headroomLifecycleMu.Unlock()
	h.headroomStart(c)
}

func (h *Handler) headroomStart(c *gin.Context) {
	h.mu.Lock()
	cfg := h.cfg
	h.mu.Unlock()

	url := "http://127.0.0.1:8787"
	if cfg.TokenSaver.Headroom.URL != "" {
		url = cfg.TokenSaver.Headroom.URL
	}
	port := headroomExtractPort(url)

	healthy := headroomHealthy(url)
	ownership, hasPID := readHeadroomPID()
	managed := hasPID && headroomOwnershipRunning(ownership)
	if !healthy && managed {
		if err := stopHeadroomPID(ownership); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"success": false, "error": fmt.Sprintf("failed to stop stale Headroom process tree: %v", err)})
			return
		}
		clearHeadroomPID()
		managed = false
	}
	ownerPID, occupied := headroomPortOwnerPID(port)
	decision := classifyHeadroomStart(healthy, ownerPID, managed)
	if decision.kind == headroomStartReuse {
		response := gin.H{"success": true, "managed": decision.managed}
		if decision.managed {
			response["pid"] = ownership.pid
		}
		c.JSON(http.StatusOK, response)
		return
	}
	if decision.kind == headroomStartConflict || occupied {
		c.JSON(http.StatusConflict, gin.H{
			"success": false,
			"error":   headroomPortConflictMessage(port, ownerPID),
			"pid":     ownerPID,
		})
		return
	}

	if !headroomInstalled() {
		c.JSON(http.StatusBadRequest, gin.H{
			"success": false,
			"error":   "Headroom CLI not installed.",
		})
		return
	}

	if ownership, ok := readHeadroomPID(); ok && headroomOwnershipRunning(ownership) {
		if err := stopHeadroomPID(ownership); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"success": false, "error": fmt.Sprintf("failed to stop stale Headroom process tree: %v", err)})
			return
		}
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
	logFile, err := os.OpenFile(headroomLogPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
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

	managedPID := cmd.Process.Pid
	identity, ok := headroomProcessIdentity(managedPID)
	if !ok {
		_ = stopProcessTree(managedPID)
		go func() { _ = cmd.Wait(); _ = logFile.Close() }()
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "error": "failed to identify started Headroom process"})
		return
	}
	if err := writeHeadroomPID(managedPID, identity); err != nil {
		cleanupErr := stopProcessTree(managedPID)
		go func() { _ = cmd.Wait(); _ = logFile.Close() }()
		if cleanupErr != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"success": false, "error": fmt.Sprintf("failed to persist Headroom process ownership: %v; cleanup failed: %v", err, cleanupErr)})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "error": fmt.Sprintf("failed to persist Headroom process ownership: %v", err)})
		return
	}

	headroomMu.Lock()
	managedProcess = &managedHeadroom{pid: managedPID, identity: identity}
	headroomMu.Unlock()

	exitCh := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		logFile.Close()
		headroomMu.Lock()
		if managedProcess != nil && managedProcess.pid == managedPID {
			managedProcess = nil
			clearHeadroomPID()
		}
		headroomMu.Unlock()
		close(exitCh)
	}()

	ready, exited := waitForHeadroomStartup(c.Request.Context(), url, headroomStartupTimeout, headroomPollInterval, headroomHealthy, exitCh)
	if exited {
		c.JSON(http.StatusInternalServerError, gin.H{
			"success": false,
			"error":   "Headroom proxy exited during startup — see headroom/proxy.log.",
		})
		return
	}
	if !ready {
		if err := stopHeadroomPID(headroomOwnership{pid: managedPID, identity: identity}); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"success": false, "error": fmt.Sprintf("Headroom did not become healthy within 30s, and process-tree cleanup failed: %v. See headroom/proxy.log.", err)})
			return
		}
		clearHeadroomPID()
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "error": "Headroom did not become healthy within 30s. Process tree stopped. See headroom/proxy.log."})
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "pid": managedPID})
}

func waitForHeadroomStartup(ctx context.Context, url string, timeout, interval time.Duration, healthy func(string) bool, exited <-chan struct{}) (bool, bool) {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-exited:
			return false, true
		default:
		}
		if healthy(url) {
			select {
			case <-exited:
				return false, true
			default:
				return true, false
			}
		}
		select {
		case <-exited:
			return false, true
		case <-ctx.Done():
			return false, false
		case <-timer.C:
			return false, false
		case <-ticker.C:
		}
	}
}

// HeadroomStop stops the managed Headroom proxy process.
func (h *Handler) HeadroomStop(c *gin.Context) {
	headroomLifecycleMu.Lock()
	defer headroomLifecycleMu.Unlock()
	ownership, ok := readHeadroomPID()
	if !ok {
		c.JSON(http.StatusOK, gin.H{"success": true, "stopped": false})
		return
	}

	if err := stopHeadroomPID(ownership); err != nil {
		c.JSON(http.StatusConflict, gin.H{"success": false, "stopped": false, "error": err.Error()})
		return
	}
	clearHeadroomPID()
	c.JSON(http.StatusOK, gin.H{"success": true, "stopped": true})
}

// HeadroomRestart stops then starts the Headroom proxy process.
func (h *Handler) HeadroomRestart(c *gin.Context) {
	headroomLifecycleMu.Lock()
	defer headroomLifecycleMu.Unlock()
	// Stop existing process
	if ownership, ok := readHeadroomPID(); ok {
		if err := stopHeadroomPID(ownership); err != nil {
			c.JSON(http.StatusConflict, gin.H{"success": false, "error": err.Error()})
			return
		}
		clearHeadroomPID()
	}

	// Start fresh — reuse HeadroomStart logic
	h.headroomStart(c)
}
