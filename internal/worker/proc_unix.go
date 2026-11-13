//go:build !windows

package worker

import (
	"os/exec"
	"syscall"
)

// configureProcess puts the worker in its own process group so a terminal
// Ctrl-C reaches only the daemon, which then shuts the worker down cleanly.
func configureProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}
