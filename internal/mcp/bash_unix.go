//go:build !windows

package mcp

import (
	"errors"
	"fmt"
	"syscall"
)

func killProcessGroup(targetPID int, removeTracking func()) (string, error) {
	pgid, err := syscall.Getpgid(targetPID)
	if err != nil {
		return "", err
	}
	if pgid > 0 && pgid != syscall.Getpgrp() {
		if err := syscall.Kill(-pgid, syscall.SIGKILL); err != nil {
			if errors.Is(err, syscall.ESRCH) {
				removeTracking()
				return fmt.Sprintf("Process %d not running; removed from tracking", targetPID), nil
			}
			return "", err
		}
		return fmt.Sprintf("Killed process group for PID %d", targetPID), nil
	}
	return "", fmt.Errorf("process group not found")
}
