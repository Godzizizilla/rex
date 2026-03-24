package notify

import (
	"fmt"
	"os/exec"
)

// Send sends a macOS notification center notification.
func Send(title, message string) error {
	script := fmt.Sprintf(`display notification %q with title %q sound name "Ping"`, message, title)
	return exec.Command("osascript", "-e", script).Run()
}
