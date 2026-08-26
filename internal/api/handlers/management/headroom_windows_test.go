//go:build windows

package management

import (
	"testing"
)

func TestParseWindowsListeningPID(t *testing.T) {
	output := []byte("  TCP    127.0.0.1:8787         0.0.0.0:0              LISTENING       22228\r\n")
	pid, ok := parseWindowsListeningPID(output, 8787)
	if !ok || pid != 22228 {
		t.Fatalf("pid = %d, ok = %v, want 22228, true", pid, ok)
	}
}

func TestWindowsProcessTreeCommand(t *testing.T) {
	name, args := processTreeStopCommand(1924)
	if name != "taskkill" {
		t.Fatalf("command = %q, want taskkill", name)
	}
	want := []string{"/PID", "1924", "/T", "/F"}
	if len(args) != len(want) {
		t.Fatalf("args = %v, want %v", args, want)
	}
	for i := range want {
		if args[i] != want[i] {
			t.Fatalf("args = %v, want %v", args, want)
		}
	}
}
