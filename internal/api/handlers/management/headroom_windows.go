//go:build windows

package management

import (
	"bytes"
	"fmt"
	"net"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/windows"
)

func setHideWindow(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
}

func processTreeStopCommand(pid int) (string, []string) {
	return "taskkill", []string{"/PID", strconv.Itoa(pid), "/T", "/F"}
}

func stopHeadroomProcessTree(pid int) error {
	name, args := processTreeStopCommand(pid)
	cmd := exec.Command(name, args...)
	setHideWindow(cmd)
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("taskkill failed: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}

func headroomManagedProcessRunning(pid int) bool {
	cmd := exec.Command("tasklist", "/FI", fmt.Sprintf("PID eq %d", pid), "/FO", "CSV", "/NH")
	setHideWindow(cmd)
	output, err := cmd.Output()
	if err != nil {
		return false
	}
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(string(output))), `"headroom.exe",`)
}

func headroomProcessIdentity(pid int) (string, bool) {
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return "", false
	}
	defer windows.CloseHandle(handle)
	var created, exited, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(handle, &created, &exited, &kernel, &user); err != nil {
		return "", false
	}
	return strconv.FormatInt(time.Unix(0, created.Nanoseconds()).UnixNano(), 10), true
}

func headroomPortOwnerPID(port int) (int, bool) {
	cmd := exec.Command("netstat", "-ano", "-p", "tcp")
	setHideWindow(cmd)
	output, err := cmd.Output()
	if err != nil {
		return 0, tcpPortOccupied(port)
	}
	pid, ok := parseWindowsListeningPID(output, port)
	return pid, ok
}

func parseWindowsListeningPID(output []byte, port int) (int, bool) {
	suffix := fmt.Sprintf(":%d", port)
	for _, line := range bytes.Split(output, []byte{'\n'}) {
		fields := strings.Fields(string(line))
		if len(fields) < 5 || !strings.EqualFold(fields[0], "TCP") || !strings.EqualFold(fields[3], "LISTENING") {
			continue
		}
		if !strings.HasSuffix(fields[1], suffix) {
			continue
		}
		pid, err := strconv.Atoi(fields[4])
		if err == nil && pid > 0 {
			return pid, true
		}
	}
	return 0, false
}

func tcpPortOccupied(port int) bool {
	listener, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return true
	}
	_ = listener.Close()
	return false
}
