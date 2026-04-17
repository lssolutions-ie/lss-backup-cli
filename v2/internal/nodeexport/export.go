// Package nodeexport collects all secrets from the node for the server's
// credential report. Used during remote node deletion so operators retain
// access to backup data after the CLI is uninstalled.
package nodeexport

import (
	"github.com/lssolutions-ie/lss-backup-cli/v2/internal/app"
	"github.com/lssolutions-ie/lss-backup-cli/v2/internal/dr"
	"github.com/lssolutions-ie/lss-backup-cli/v2/internal/jobs"
	"github.com/lssolutions-ie/lss-backup-cli/v2/internal/sshcreds"
)

// SecretsExport is the one-time payload sent to the server when
// export_secrets is requested. Contains every credential needed to
// access backup data independently using plain restic/rsync.
type SecretsExport struct {
	Jobs     []JobSecrets  `json:"jobs"`
	DRBackup *DRSecrets    `json:"dr_backup,omitempty"`
	SSHUser  string        `json:"ssh_user,omitempty"`
	SSHPass  string        `json:"ssh_password,omitempty"`
}

// JobSecrets holds credentials for a single backup job.
type JobSecrets struct {
	JobID              string `json:"job_id"`
	JobName            string `json:"job_name"`
	Program            string `json:"program"`
	DestinationType    string `json:"destination_type"`
	DestinationPath    string `json:"destination_path"`
	DestinationHost    string `json:"destination_host,omitempty"`
	DestinationShare   string `json:"destination_share,omitempty"`
	ResticPassword     string `json:"restic_password,omitempty"`
	AWSAccessKeyID     string `json:"aws_access_key_id,omitempty"`
	AWSSecretAccessKey string `json:"aws_secret_access_key,omitempty"`
	AWSRegion          string `json:"aws_region,omitempty"`
	SMBPassword        string `json:"smb_password,omitempty"`
	SMBDestPassword    string `json:"smb_dest_password,omitempty"`
	NFSPassword        string `json:"nfs_password,omitempty"`
	NFSDestPassword    string `json:"nfs_dest_password,omitempty"`
}

// DRSecrets holds credentials for the disaster recovery backup.
type DRSecrets struct {
	S3Endpoint     string `json:"s3_endpoint"`
	S3Bucket       string `json:"s3_bucket"`
	S3Region       string `json:"s3_region,omitempty"`
	NodeFolder     string `json:"node_folder"`
	ResticPassword string `json:"restic_password"`
	AWSAccessKeyID string `json:"aws_access_key_id"`
	AWSSecretKey   string `json:"aws_secret_access_key"`
}

// Collect gathers all secrets from the node. Best-effort — if a job's
// secrets can't be read, it's included with empty credential fields.
func Collect(paths app.Paths, psk string) SecretsExport {
	export := SecretsExport{}

	// Jobs
	allJobs, err := jobs.LoadAll(paths)
	if err == nil {
		for _, job := range allJobs {
			js := JobSecrets{
				JobID:           job.ID,
				JobName:         job.Name,
				Program:         job.Program,
				DestinationType: job.Destination.Type,
				DestinationPath: job.Destination.Path,
				DestinationHost: job.Destination.Host,
				DestinationShare: job.Destination.ShareName,
			}
			// Secrets from secrets.env (already loaded by jobs.Load)
			js.ResticPassword = job.Secrets.ResticPassword
			js.AWSAccessKeyID = job.Secrets.AWSAccessKeyID
			js.AWSSecretAccessKey = job.Secrets.AWSSecretAccessKey
			js.AWSRegion = job.Secrets.AWSDefaultRegion
			js.SMBPassword = job.Secrets.SMBPassword
			js.SMBDestPassword = job.Secrets.SMBDestPassword
			js.NFSPassword = job.Secrets.NFSPassword
			js.NFSDestPassword = job.Secrets.NFSDestPassword
			export.Jobs = append(export.Jobs, js)
		}
	}

	// DR backup config (cached locally, encrypted with PSK)
	if mgr := dr.Global(); mgr != nil {
		if cfg := mgr.GetConfig(); cfg != nil && cfg.Enabled {
			export.DRBackup = &DRSecrets{
				S3Endpoint:     cfg.S3Endpoint,
				S3Bucket:       cfg.S3Bucket,
				S3Region:       cfg.S3Region,
				NodeFolder:     cfg.NodeFolder,
				ResticPassword: cfg.ResticPassword,
				AWSAccessKeyID: cfg.S3AccessKey,
				AWSSecretKey:   cfg.S3SecretKey,
			}
		}
	}

	// SSH credentials (encrypted with PSK)
	if psk != "" && sshcreds.Exists(paths.RootDir) {
		if creds, err := sshcreds.Load(paths.RootDir, psk); err == nil {
			export.SSHUser = creds.Username
			export.SSHPass = creds.Password
		}
	}

	return export
}
