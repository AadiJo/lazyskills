//go:build windows

package runner

import (
	"os/exec"
	"strconv"
)

func configureProcessGroup(_ *exec.Cmd) {}

func killProcessTree(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	_ = exec.Command("taskkill", "/PID", strconv.Itoa(cmd.Process.Pid), "/T", "/F").Run()
	_ = cmd.Process.Kill()
}
