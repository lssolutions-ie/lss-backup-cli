package cli

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/lssolutions-ie/lss-backup-cli/v2/internal/app"
	"github.com/lssolutions-ie/lss-backup-cli/v2/internal/mount"
)

type mkdirResult struct {
	Path    string `json:"path"`
	Created bool   `json:"created"`
}

// runDestMkdir mounts a network share, creates a directory, and unmounts.
// Used by the server dashboard wizard to create destination folders.
func runDestMkdir(_ app.Paths, args []string) error {
	fs := flag.NewFlagSet("dest-mkdir", flag.ContinueOnError)
	destType := fs.String("dest-type", "", "destination type: smb | nfs [required]")
	destHost := fs.String("dest-host", "", "SMB/NFS host [required]")
	destShare := fs.String("dest-share", "", "SMB/NFS share name [required]")
	destUsername := fs.String("dest-username", "", "SMB username")
	destDomain := fs.String("dest-domain", "", "SMB domain")
	destPasswordStdin := fs.Bool("dest-password-stdin", false, "read destination password from stdin")
	subPath := fs.String("path", "", "directory to create within the share [required]")
	if err := fs.Parse(args); err != nil {
		return err
	}

	if *destType == "" || (*destType != "smb" && *destType != "nfs") {
		return fmt.Errorf("dest-mkdir: --dest-type must be smb or nfs")
	}
	if *destHost == "" {
		return fmt.Errorf("dest-mkdir: --dest-host is required")
	}
	if *destShare == "" {
		return fmt.Errorf("dest-mkdir: --dest-share is required")
	}
	if *subPath == "" {
		return fmt.Errorf("dest-mkdir: --path is required")
	}

	password := ""
	if *destPasswordStdin {
		reader := bufio.NewReader(os.Stdin)
		pw, err := reader.ReadString('\n')
		if err != nil && err != io.EOF {
			return fmt.Errorf("read password from stdin: %w", err)
		}
		password = strings.TrimRight(pw, "\r\n")
	}

	mountPoint := filepath.Join(mount.MountBasePath, "mkdir-tmp")
	spec := mount.Spec{
		Type:       *destType,
		Host:       *destHost,
		ShareName:  *destShare,
		Username:   *destUsername,
		Password:   password,
		Domain:     *destDomain,
		MountPoint: mountPoint,
	}

	if err := mount.Mount(spec); err != nil {
		return fmt.Errorf("mount failed: %w", err)
	}
	defer mount.Unmount(mountPoint)

	targetPath := filepath.Join(mountPoint, *subPath)
	if err := os.MkdirAll(targetPath, 0o755); err != nil {
		return fmt.Errorf("mkdir failed: %w", err)
	}

	result := mkdirResult{
		Path:    *subPath,
		Created: true,
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(result)
}
