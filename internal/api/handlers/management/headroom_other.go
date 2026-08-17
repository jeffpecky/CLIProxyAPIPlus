//go:build !windows

package management

import "os/exec"

func setHideWindow(_ *exec.Cmd) {}
