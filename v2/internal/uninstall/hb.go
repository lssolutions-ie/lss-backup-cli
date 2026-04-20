package uninstall

import (
	"fmt"
	"os"
	"time"

	"github.com/lssolutions-ie/lss-backup-cli/v2/internal/config"
	"github.com/lssolutions-ie/lss-backup-cli/v2/internal/platform"
	"github.com/lssolutions-ie/lss-backup-cli/v2/internal/reporting"
	"github.com/lssolutions-ie/lss-backup-cli/v2/internal/version"
)

// sendUninstallCompleteHB fires the final positive-confirmation heartbeat
// at the end of the uninstall flow. The server uses the resulting record
// to decide whether to DeleteNode(). If delivery fails entirely the row
// stays in uninstall_pending — operator retries. No silent data loss,
// no false-positive DeleteNode().
//
// Must be called BEFORE the state dir is wiped: it reads AppConfig for
// the PSK, server URL, and node ID. Delivery goes over direct HTTPS to
// the server's public URL, not through the reverse tunnel, so the
// tunnel state at the moment of the POST does not matter.
//
// Retries once on non-OK response / network error, then gives up.
func sendUninstallCompleteHB(paths platform.RuntimePaths, retainedData, cleanupSucceeded bool, details string) {
	cfg, err := config.LoadAppConfig(paths.ConfigDir)
	if err != nil || !cfg.Enabled || cfg.ServerURL == "" || cfg.NodeID == "" || len(cfg.PSKKey) != 128 {
		fmt.Println("Final heartbeat: node not registered with a server, skipping.")
		return
	}

	status := reporting.NodeStatus{
		PayloadVersion: "3",
		ReportType:     reporting.ReportTypeUninstallComplete,
		ReportedAt:     time.Now().UTC(),
		CLIVersion:     version.Current,
		Jobs:           []reporting.JobStatus{},
		Uninstall: &reporting.UninstallReport{
			RetainedData:     retainedData,
			CleanupSucceeded: cleanupSucceeded,
			CleanupDetails:   details,
		},
	}
	if h, err := os.Hostname(); err == nil {
		status.NodeName = h
	}

	reporter := reporting.NewReporter(cfg, paths.ConfigDir, paths.LogsDir)

	fmt.Println("Sending uninstall_complete heartbeat...")
	for attempt := 1; attempt <= 2; attempt++ {
		resp := reporter.ReportSync(status)
		if resp.OK {
			fmt.Println("Uninstall_complete heartbeat acknowledged by server.")
			return
		}
		if attempt < 2 {
			time.Sleep(3 * time.Second)
		}
	}
	fmt.Println("Warning: uninstall_complete heartbeat not acknowledged after retry — server may leave row in uninstall_pending.")
}
