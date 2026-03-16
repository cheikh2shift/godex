//go:build windows

package mcp

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

func killProcessGroup(targetPID int, removeTracking func()) (string, error) {
	// On Windows, we use os.FindProcess and Kill on the process
	// Process groups work differently on Windows
	proc, err := os.FindProcess(targetPID)
	if err != nil {
		removeTracking()
		return "", fmt.Errorf("process not found: %w", err)
	}
	if err := proc.Kill(); err != nil {
		if errors.Is(err, syscall.ESRCH) {
			removeTracking()
			return fmt.Sprintf("Process %d not running; removed from tracking", targetPID), nil
		}
		return "", err
	}
	return fmt.Sprintf("Killed process %d", targetPID), nil
}


// GetUnixTools returns an empty list on Windows (not supported)
func GetUnixTools() []Tool {
	return []Tool{}
}


// HandleRunBashScript is not supported on Windows
func (s *BashServer) HandleRunBashScript(ctx context.Context, args map[string]interface{}) (string, error) {
	return "", fmt.Errorf("run_bash_script is not supported on Windows")
}
