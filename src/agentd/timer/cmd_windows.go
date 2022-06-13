//go:build windows
// +build windows

package timer

import (
	"fmt"
	"os/exec"
)

func CmdStart(cmd *exec.Cmd) error {
	return cmd.Start()
}

func CmdKill(cmd *exec.Cmd) error {
	return exec.Command("taskkill", "/F", "/T", "/PID", fmt.Sprint(cmd.Process.Pid)).Run()
	// return cmd.Process.Kill()
}
