package webserver

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
)

// OpenBrowser opens a URL without a shell and includes a WSL-to-Windows path.
func OpenBrowser(target string) error {
	if strings.Contains(target, "'") || strings.ContainsAny(target, "\r\n") {
		return fmt.Errorf("unsafe browser URL")
	}
	if runtime.GOOS == "linux" && isWSL() {
		powershell := "/mnt/c/Windows/System32/WindowsPowerShell/v1.0/powershell.exe"
		if _, err := os.Stat(powershell); err == nil {
			return exec.Command(powershell, "-NoProfile", "-Command", "Start-Process -FilePath '"+target+"'").Start()
		}
	}
	var command string
	var args []string
	switch runtime.GOOS {
	case "darwin":
		command, args = "open", []string{target}
	case "windows":
		command, args = "rundll32", []string{"url.dll,FileProtocolHandler", target}
	default:
		command, args = "xdg-open", []string{target}
	}
	return exec.Command(command, args...).Start()
}

func isWSL() bool {
	if os.Getenv("WSL_DISTRO_NAME") != "" || os.Getenv("WSL_INTEROP") != "" {
		return true
	}
	b, _ := os.ReadFile("/proc/version")
	return strings.Contains(strings.ToLower(string(b)), "microsoft")
}
