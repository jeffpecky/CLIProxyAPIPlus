package management

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

// headroomCompressionExtras lists the extras that improve compression quality.
var headroomCompressionExtras = []string{"code", "ml"}

// headroomExtraMarkers maps each extra to the pip packages that indicate it's installed.
var headroomExtraMarkers = map[string][]string{
	"code": {"tree-sitter", "tree-sitter-language-pack"},
	"ml":   {"torch", "huggingface-hub"},
}

const headroomPipTimeoutMs = 8000

var (
	installLogPath = filepath.Join("headroom", "install.log")
	installLogMu   sync.Mutex
)

// pipPackage represents a package from pip list --format=json.
type pipPackage struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// headroomExtrasStatus represents the installation status of headroom extras.
type headroomExtrasStatus struct {
	Installed bool            `json:"installed"`
	Version   string          `json:"version,omitempty"`
	Extras    map[string]bool `json:"extras"`
}

// getInstalledHeadroomExtras detects which headroom extras are installed via pip list.
func getInstalledHeadroomExtras() headroomExtrasStatus {
	python := findPython()
	if python == "" {
		return headroomExtrasStatus{Installed: false, Extras: map[string]bool{"code": false, "ml": false}}
	}

	ctx, cancel := context.WithTimeout(context.Background(), headroomPipTimeoutMs*time.Millisecond)
	defer cancel()

	cmd := exec.CommandContext(ctx, python, "-m", "pip", "list", "--format=json", "--disable-pip-version-check")
	setHideWindow(cmd)
	output, err := cmd.Output()
	if err != nil {
		return headroomExtrasStatus{Installed: false, Extras: map[string]bool{"code": false, "ml": false}}
	}

	var packages []pipPackage
	if err := json.Unmarshal(output, &packages); err != nil {
		return headroomExtrasStatus{Installed: false, Extras: map[string]bool{"code": false, "ml": false}}
	}

	// Build set of installed package names (lowercase).
	installed := make(map[string]bool, len(packages))
	for _, p := range packages {
		installed[strings.ToLower(p.Name)] = true
	}

	// Check if headroom-ai itself is installed.
	if !installed["headroom-ai"] {
		return headroomExtrasStatus{Installed: false, Extras: map[string]bool{"code": false, "ml": false}}
	}

	// Find headroom version.
	var version string
	for _, p := range packages {
		if strings.ToLower(p.Name) == "headroom-ai" {
			version = p.Version
			break
		}
	}

	// Check which extras are installed via marker packages.
	extras := make(map[string]bool, len(headroomCompressionExtras))
	for _, extra := range headroomCompressionExtras {
		markers := headroomExtraMarkers[extra]
		for _, marker := range markers {
			if installed[strings.ToLower(marker)] {
				extras[extra] = true
				break
			}
		}
		if !extras[extra] {
			extras[extra] = false
		}
	}

	return headroomExtrasStatus{
		Installed: true,
		Version:   version,
		Extras:    extras,
	}
}

// HeadroomExtrasStatus returns the current extras installation status.
func (h *Handler) HeadroomExtrasStatus(c *gin.Context) {
	// Support ?log=1 for live install log polling
	if c.Query("log") == "1" {
		log := readInstallLogTail(15)
		c.JSON(200, gin.H{"log": log})
		return
	}
	status := getInstalledHeadroomExtras()
	c.JSON(200, status)
}

// HeadroomInstallExtras installs headroom extras (code, ml).
func (h *Handler) HeadroomInstallExtras(c *gin.Context) {
	var body struct {
		Extras []string `json:"extras"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || len(body.Extras) == 0 {
		c.JSON(400, gin.H{"success": false, "error": "No extras specified."})
		return
	}

	// Validate extras.
	for _, extra := range body.Extras {
		if extra != "code" && extra != "ml" {
			c.JSON(400, gin.H{"success": false, "error": fmt.Sprintf("Unknown extra: %s. Valid extras: code, ml", extra)})
			return
		}
	}

	// Build install spec.
	spec := fmt.Sprintf("headroom-ai[proxy,%s]", strings.Join(body.Extras, ","))

	python := findPython()
	if python == "" {
		c.JSON(500, gin.H{"success": false, "error": "Python not found. Install Python 3.10+ first."})
		return
	}

	// Ensure headroom directory exists
	if err := os.MkdirAll(filepath.Dir(installLogPath), 0o700); err != nil {
		c.JSON(500, gin.H{"success": false, "error": fmt.Sprintf("Failed to create log directory: %v", err)})
		return
	}

	// Truncate log file for fresh output
	logFile, err := os.Create(installLogPath)
	if err != nil {
		c.JSON(500, gin.H{"success": false, "error": fmt.Sprintf("Failed to create log file: %v", err)})
		return
	}
	logFile.Close()

	// Run pip install asynchronously (non-blocking)
	args := []string{"-m", "pip", "install", "--upgrade", spec}
	cmd := exec.Command(python, args...)
	setHideWindow(cmd)

	logFile, err = os.OpenFile(installLogPath, os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		c.JSON(500, gin.H{"success": false, "error": fmt.Sprintf("Failed to open log file: %v", err)})
		return
	}
	cmd.Stdout = logFile
	cmd.Stderr = logFile

	if err := cmd.Start(); err != nil {
		logFile.Close()
		c.JSON(500, gin.H{"success": false, "error": fmt.Sprintf("Failed to start pip install: %v", err)})
		return
	}

	// Return immediately - log polling will show progress
	c.JSON(200, gin.H{"success": true, "spec": spec, "extras": body.Extras, "status": "installing"})

	// Wait in background and close log file when done
	go func() {
		_ = cmd.Wait()
		logFile.Close()
	}()
}

// HeadroomUninstallExtra uninstalls a headroom extra and its packages.
func (h *Handler) HeadroomUninstallExtra(c *gin.Context) {
	extra := c.Param("extra")
	if extra != "code" && extra != "ml" {
		c.JSON(400, gin.H{"success": false, "error": fmt.Sprintf("Unknown extra: %s. Valid extras: code, ml", extra)})
		return
	}

	markers := headroomExtraMarkers[extra]
	if len(markers) == 0 {
		c.JSON(400, gin.H{"success": false, "error": fmt.Sprintf("No packages to uninstall for extra: %s", extra)})
		return
	}

	python := findPython()
	if python == "" {
		c.JSON(500, gin.H{"success": false, "error": "Python not found."})
		return
	}

	// Ensure headroom directory exists
	if err := os.MkdirAll(filepath.Dir(installLogPath), 0o700); err != nil {
		c.JSON(500, gin.H{"success": false, "error": fmt.Sprintf("Failed to create log directory: %v", err)})
		return
	}

	// Truncate log file for fresh output
	logFile, err := os.Create(installLogPath)
	if err != nil {
		c.JSON(500, gin.H{"success": false, "error": fmt.Sprintf("Failed to create log file: %v", err)})
		return
	}
	logFile.Close()

	// Run pip uninstall asynchronously (non-blocking)
	args := append([]string{"-m", "pip", "uninstall", "-y"}, markers...)
	cmd := exec.Command(python, args...)
	setHideWindow(cmd)

	logFile, err = os.OpenFile(installLogPath, os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		c.JSON(500, gin.H{"success": false, "error": fmt.Sprintf("Failed to open log file: %v", err)})
		return
	}
	cmd.Stdout = logFile
	cmd.Stderr = logFile

	if err := cmd.Start(); err != nil {
		logFile.Close()
		c.JSON(500, gin.H{"success": false, "error": fmt.Sprintf("Failed to start pip uninstall: %v", err)})
		return
	}

	// Return immediately - log polling will show progress
	c.JSON(200, gin.H{"success": true, "removed": markers, "extras": []string{extra}, "status": "uninstalling"})

	// Wait in background and close log file when done
	go func() {
		_ = cmd.Wait()
		logFile.Close()
	}()
}

// readInstallLogTail reads the last N lines of the install log file.
func readInstallLogTail(maxLines int) string {
	installLogMu.Lock()
	defer installLogMu.Unlock()

	f, err := os.Open(installLogPath)
	if err != nil {
		return ""
	}
	defer f.Close()

	var lines []string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}

	if len(lines) > maxLines {
		lines = lines[len(lines)-maxLines:]
	}
	return strings.Join(lines, "\n")
}

// findPython locates a Python 3.10+ interpreter.
func findPython() string {
	candidates := []string{"python3.13", "python3.12", "python3.11", "python3.10", "python3", "python"}
	for _, name := range candidates {
		if path, err := exec.LookPath(name); err == nil && path != "" {
			return path
		}
	}
	return ""
}
