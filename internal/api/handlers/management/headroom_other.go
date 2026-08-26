//go:build !windows

package management

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

func setHideWindow(_ *exec.Cmd) {}

func headroomManagedProcessRunning(pid int) bool {
	cmd := exec.Command("ps", "-p", fmt.Sprintf("%d", pid), "-o", "comm=")
	output, err := cmd.Output()
	return err == nil && strings.Contains(strings.ToLower(filepath.Base(strings.TrimSpace(string(output)))), "headroom")
}

func headroomProcessIdentity(pid int) (string, bool) {
	cmd := exec.Command("ps", "-p", fmt.Sprintf("%d", pid), "-o", "lstart=")
	output, err := cmd.Output()
	identity := strings.TrimSpace(string(output))
	return identity, err == nil && identity != ""
}

func stopHeadroomProcessTree(pid int) error {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	_ = proc.Signal(syscall.SIGTERM)
	for i := 0; i < 30; i++ {
		time.Sleep(100 * time.Millisecond)
		if !headroomManagedProcessRunning(pid) {
			return nil
		}
	}
	return proc.Kill()
}

func headroomPortOwnerPID(port int) (int, bool) {
	listener, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return -1, true
	}
	_ = listener.Close()
	return 0, false
}
