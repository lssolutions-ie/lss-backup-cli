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

// runDestBrowse mounts a network share temporarily, lists a directory,
// and unmounts. Used by the server dashboard's backup wizard to browse
// SMB/NFS destinations visually.
func runDestBrowse(_ app.Paths, args []string) error {
	fs := flag.NewFlagSet("dest-browse", flag.ContinueOnError)
	destType := fs.String("dest-type", "", "destination type: smb | nfs [required]")
	destHost := fs.String("dest-host", "", "SMB/NFS host [required]")
	destShare := fs.String("dest-share", "", "SMB/NFS share name [required]")
	destUsername := fs.String("dest-username", "", "SMB username")
	destDomain := fs.String("dest-domain", "", "SMB domain")
	destPasswordStdin := fs.Bool("dest-password-stdin", false, "read destination password from stdin")
	subPath := fs.String("path", "/", "subdirectory to browse within the share")
	if err := fs.Parse(args); err != nil {
		return err
	}

	if *destType == "" {
		return fmt.Errorf("dest-browse: --dest-type is required (smb or nfs)")
	}
	if *destType != "smb" && *destType != "nfs" {
		return fmt.Errorf("dest-browse: --dest-type must be smb or nfs (use --browse-path for local)")
	}
	if *destHost == "" {
		return fmt.Errorf("dest-browse: --dest-host is required")
	}
	if *destShare == "" {
		return fmt.Errorf("dest-browse: --dest-share is required")
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

	// Use a temporary mount point.
	mountPoint := filepath.Join(mount.MountBasePath, "browse-tmp")

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

	// Browse the subdirectory within the mount.
	browsePath := mountPoint
	if *subPath != "" && *subPath != "/" {
		browsePath = filepath.Join(mountPoint, *subPath)
	}

	info, err := os.Stat(browsePath)
	if err != nil {
		return fmt.Errorf("path not found on share: %s", *subPath)
	}
	if !info.IsDir() {
		return fmt.Errorf("not a directory: %s", *subPath)
	}

	entries, err := os.ReadDir(browsePath)
	if err != nil {
		return fmt.Errorf("cannot read directory: %w", err)
	}

	result := browseResult{
		Path:    *subPath,
		Entries: make([]browseEntry, 0, len(entries)),
	}

	for _, e := range entries {
		fi, err := e.Info()
		if err != nil {
			continue
		}
		typ := "file"
		if fi.IsDir() {
			typ = "dir"
		}
		result.Entries = append(result.Entries, browseEntry{
			Name:  e.Name(),
			Type:  typ,
			Size:  fi.Size(),
			Perms: fi.Mode().Perm().String(),
		})
	}

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(result)
}
