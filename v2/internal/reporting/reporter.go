package reporting

// ReportResponse holds fields from the server's JSON response.
type ReportResponse struct {
	OK                  bool   `json:"ok"`
	TunnelKeyRegistered bool   `json:"tunnel_key_registered"`
	// AuditAckSeq is the highest audit event seq the server has durably
	// persisted for this node. Reporter uses it to trim the local queue.
	// Missing/zero means the server hasn't acked anything (keep queue).
	AuditAckSeq uint64 `json:"audit_ack_seq,omitempty"`
	// ReconcileRepoStats is the list of job IDs for which the server wants
	// fresh restic repo statistics. The daemon runs `restic stats` for each
	// and attaches repo_size_bytes to Jobs[].result on the next heartbeat.
	// Empty/missing means no request — no stats calls, no cost.
	ReconcileRepoStats []string `json:"reconcile_repo_stats,omitempty"`
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
