package cli

import (
	"fmt"
	"os"
	"os/exec"

	"github.com/lssolutions-ie/lss-backup-cli/v2/internal/engines"
	"github.com/lssolutions-ie/lss-backup-cli/v2/internal/jobs"
	"github.com/lssolutions-ie/lss-backup-cli/v2/internal/mount"
)

// validateDestination checks that the destination is reachable before
// creating the job. Returns an error if the destination is invalid.
func validateDestination(input jobs.CreateInput) error {
	switch input.DestType {
	case "local":
		return validateLocalDest(input.DestPath)
	case "s3":
		return validateS3Dest(input)
	case "smb":
		return validateSMBDest(input)
	case "nfs":
		return validateNFSDest(input)
	}
	return nil
}

func validateLocalDest(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			// Try to create it — operator may want the CLI to create the dir.
			if mkErr := os.MkdirAll(path, 0o755); mkErr != nil {
				return fmt.Errorf("destination path does not exist and cannot be created: %s", path)
			}
			fmt.Printf("  Created destination directory: %s\n", path)
			return nil
		}
		return fmt.Errorf("cannot access destination path: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("destination path is not a directory: %s", path)
	}
	return nil
}

func validateS3Dest(input jobs.CreateInput) error {
	resticBin, err := engines.LookResticPath()
	if err != nil {
		return fmt.Errorf("restic not found: %w", err)
	}

	// Try to access the repo — `restic cat config` succeeds if the repo
	// exists, fails with a specific error if it doesn't. Either way, it
	// validates that the S3 endpoint + credentials are reachable.
	env := os.Environ()
	if input.Secrets != nil {
		env = append(env,
			"RESTIC_PASSWORD="+input.Secrets.ResticPassword,
			"AWS_ACCESS_KEY_ID="+input.Secrets.AWSAccessKeyID,
			"AWS_SECRET_ACCESS_KEY="+input.Secrets.AWSSecretAccessKey,
		)
		if input.Secrets.AWSDefaultRegion != "" {
			env = append(env, "AWS_DEFAULT_REGION="+input.Secrets.AWSDefaultRegion)
		}
	}

	fmt.Println("  Validating S3 destination...")
	cmd := exec.Command(resticBin, "-r", input.DestPath, "cat", "config")
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	if err != nil {
		// "Is there a repository" means creds work but repo doesn't exist yet — that's fine.
		outStr := string(out)
		if contains(outStr, "Is there a repository", "unable to open config") {
			fmt.Println("  S3 destination reachable (repo will be initialized on first backup)")
			return nil
		}
		return fmt.Errorf("S3 destination unreachable: %s", outStr)
	}
	fmt.Println("  S3 destination validated (repo exists)")
	return nil
}

func validateSMBDest(input jobs.CreateInput) error {
	password := ""
	if input.Secrets != nil {
		password = input.Secrets.SMBDestPassword
	}

	mountPoint := mount.DestMountPoint("validate-tmp", input.DestHost, input.DestShareName)
	spec := mount.Spec{
		Type:       "smb",
		Host:       input.DestHost,
		ShareName:  input.DestShareName,
		Username:   input.DestUsername,
		Password:   password,
		Domain:     input.DestDomain,
		MountPoint: mountPoint,
	}

	fmt.Println("  Validating SMB destination...")
	if err := mount.Mount(spec); err != nil {
		return fmt.Errorf("SMB destination unreachable: %w", err)
	}
	mount.Unmount(mountPoint)
	fmt.Println("  SMB destination validated")
	return nil
}

func validateNFSDest(input jobs.CreateInput) error {
	mountPoint := mount.DestMountPoint("validate-tmp", input.DestHost, input.DestShareName)
	spec := mount.Spec{
		Type:       "nfs",
		Host:       input.DestHost,
		ShareName:  input.DestShareName,
		MountPoint: mountPoint,
	}

	fmt.Println("  Validating NFS destination...")
	if err := mount.Mount(spec); err != nil {
		return fmt.Errorf("NFS destination unreachable: %w", err)
	}
	mount.Unmount(mountPoint)
	fmt.Println("  NFS destination validated")
	return nil
}

func contains(s string, substrs ...string) bool {
	for _, sub := range substrs {
		if len(sub) > 0 && len(s) >= len(sub) {
			for i := 0; i <= len(s)-len(sub); i++ {
				if s[i:i+len(sub)] == sub {
					return true
				}
			}
		}
	}
	return false
}
