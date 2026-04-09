package activitylog

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

const filename = "activity.log"

// Log appends a timestamped entry to {logsDir}/activity.log.
// Failures are silently discarded — logging must never interrupt user flow.
func Log(logsDir, message string) {
	if err := os.MkdirAll(logsDir, 0o755); err != nil {
		return
	}
	f, err := os.OpenFile(filepath.Join(logsDir, filename), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "%s  %s\n", time.Now().Format("2006-01-02 15:04:05"), message)
}
