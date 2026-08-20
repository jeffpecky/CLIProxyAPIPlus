package management

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
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

	ctx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Minute)
	defer cancel()

	python := findPython()
	if python == "" {
		c.JSON(500, gin.H{"success": false, "error": "Python not found. Install Python 3.10+ first."})
		return
	}

	// Try pip install with --break-system-packages first, then --user.
	cmd := exec.CommandContext(ctx, python, "-m", "pip", "install", "--break-system-packages", "--upgrade", spec)
	output, err := cmd.CombinedOutput()
	if err == nil {
		c.JSON(200, gin.H{"success": true, "method": "pip-system", "output": string(output)})
		return
	}

	// Try --user.
	cmd2 := exec.CommandContext(ctx, python, "-m", "pip", "install", "--user", "--upgrade", spec)
	output2, err2 := cmd2.CombinedOutput()
	if err2 == nil {
		c.JSON(200, gin.H{"success": true, "method": "pip-user", "output": string(output2)})
		return
	}

	c.JSON(500, gin.H{
		"success": false,
		"error":   fmt.Sprintf("Install failed. Last error: %v\n%s", err2, string(output2)),
	})
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

	ctx, cancel := context.WithTimeout(c.Request.Context(), 2*time.Minute)
	defer cancel()

	// Uninstall marker packages.
	args := append([]string{"-m", "pip", "uninstall", "-y"}, markers...)
	cmd := exec.CommandContext(ctx, python, args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		c.JSON(500, gin.H{"success": false, "error": fmt.Sprintf("Uninstall failed: %v\n%s", err, string(output))})
		return
	}

	c.JSON(200, gin.H{"success": true, "output": string(output)})
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
