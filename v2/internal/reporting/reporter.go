package reporting

// DRConfig is the disaster-recovery configuration pushed by the server.
// When present and enabled, the CLI backs up its own config to S3 on a
// schedule. See internal/dr for the executor.
type DRConfig struct {
	Enabled        bool   `json:"enabled"`
	S3Endpoint     string `json:"s3_endpoint"`
	S3Bucket       string `json:"s3_bucket"`
	S3Region       string `json:"s3_region"`
	S3AccessKey    string `json:"s3_access_key"`
	S3SecretKey    string `json:"s3_secret_key"`
	ResticPassword string `json:"restic_password"`
	NodeFolder     string `json:"node_folder"`
	IntervalHours  int    `json:"interval_hours"`
}

// ReportResponse holds fields from the server's JSON response.
type ReportResponse struct {
	OK                  bool      `json:"ok"`
	TunnelKeyRegistered bool      `json:"tunnel_key_registered"`
	AuditAckSeq         uint64    `json:"audit_ack_seq,omitempty"`
	ReconcileRepoStats  []string  `json:"reconcile_repo_stats,omitempty"`
	// DRConfig is pushed when the server wants this node to start/update
	// DR backups. Omitted when unchanged or not enabled.
	DRConfig            *DRConfig `json:"dr_config,omitempty"`
	// DRForceRun is set when an operator clicks "Run Now" on the shield.
	DRForceRun          bool      `json:"dr_force_run,omitempty"`
}

// Reporter sends the current node status snapshot to a management server.
// All implementations must be fire-and-forget: a failed report must never
// block a backup job or propagate an error to the caller.
type Reporter interface {
	Report(status NodeStatus)
	// ReportSync sends the status synchronously, blocking until complete.
	// Returns the server response so callers can check tunnel_key_registered.
	ReportSync(status NodeStatus) ReportResponse
}

// NoOpReporter is used when reporting is disabled.
type NoOpReporter struct{}

func (NoOpReporter) Report(status NodeStatus)                {}
func (NoOpReporter) ReportSync(status NodeStatus) ReportResponse { return ReportResponse{} }
