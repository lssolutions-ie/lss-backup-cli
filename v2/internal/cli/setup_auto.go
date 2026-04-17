package cli

import (
	"fmt"
	"os"
	"runtime"

	"github.com/lssolutions-ie/lss-backup-cli/v2/internal/app"
	"github.com/lssolutions-ie/lss-backup-cli/v2/internal/config"
	"github.com/lssolutions-ie/lss-backup-cli/v2/internal/sshcreds"
	"github.com/lssolutions-ie/lss-backup-cli/v2/internal/version"
)

// runSetupAuto performs a non-interactive, fully automated node setup:
// writes management console config, creates SSH credentials, prints a
// summary. Used by the server-assisted one-command install path.
//
// Called when install-cli.sh passes LSS_SERVER_URL + LSS_NODE_UID +
// LSS_PSK_KEY environment variables into `lss-backup-cli --setup-auto`.
func runSetupAuto(paths app.Paths) error {
	serverURL := os.Getenv("LSS_SERVER_URL")
	nodeUID := os.Getenv("LSS_NODE_UID")
	pskKey := os.Getenv("LSS_PSK_KEY")

	if serverURL == "" || nodeUID == "" || pskKey == "" {
		return fmt.Errorf("--setup-auto requires LSS_SERVER_URL, LSS_NODE_UID, and LSS_PSK_KEY environment variables")
	}

	// 1. Write management console config.
	hostname, _ := os.Hostname()
	cfg := config.AppConfig{
		Enabled:      true,
		ServerURL:    serverURL,
		NodeID:       nodeUID,
		PSKKey:       pskKey,
		NodeHostname: hostname,
	}
	if err := config.SaveAppConfig(paths.RootDir, cfg); err != nil {
		return fmt.Errorf("write config.toml: %w", err)
	}
	fmt.Println("  Management console configured.")

	// 2. Create SSH credentials (non-interactive).
	sshUser := ""
	if !sshcreds.Exists(paths.RootDir) {
		creds, err := sshcreds.GenerateCredentials()
		if err != nil {
			return fmt.Errorf("generate SSH credentials: %w", err)
		}
		if err := sshcreds.CreateUser(creds); err != nil {
			return fmt.Errorf("create SSH user: %w", err)
		}
		// Encrypt with PSK so the server can decrypt if needed.
		if err := sshcreds.Save(paths.RootDir, creds, pskKey); err != nil {
			return fmt.Errorf("save SSH credentials: %w", err)
		}
		sshUser = creds.Username
		fmt.Printf("  SSH user %s created.\n", creds.Username)
	} else {
		fmt.Println("  SSH credentials already exist — skipping.")
		// Try to read existing username for the summary.
		if creds, err := sshcreds.Load(paths.RootDir, pskKey); err == nil {
			sshUser = creds.Username
		}
	}

	// 3. Print summary banner.
	fmt.Println()
	fmt.Println("══════════════════════════════════════════════════")
	fmt.Println("  Installation Complete")
	fmt.Println("══════════════════════════════════════════════════")
	fmt.Println()
	fmt.Printf("  Version:     %s\n", version.Current)
	fmt.Printf("  Node ID:     %s\n", nodeUID)
	fmt.Printf("  Server:      %s\n", serverURL)
	if sshUser != "" {
		fmt.Printf("  SSH User:    %s\n", sshUser)
	}
	fmt.Printf("  Platform:    %s/%s\n", runtime.GOOS, runtime.GOARCH)
	fmt.Println("  Daemon:      will start momentarily")
	fmt.Println()

	if runtime.GOOS == "darwin" {
		fmt.Println("  ⚠ macOS: Grant Full Disk Access")
		fmt.Println("    System Settings > Privacy & Security > Full Disk Access")
		fmt.Printf("    Add: %s\n", os.Args[0])
		fmt.Println()
	}
	fmt.Println("══════════════════════════════════════════════════")

	return nil
}
