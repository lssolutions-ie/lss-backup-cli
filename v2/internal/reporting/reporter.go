package reporting

// ReportResponse holds fields from the server's JSON response.
type ReportResponse struct {
	OK                  bool `json:"ok"`
	TunnelKeyRegistered bool `json:"tunnel_key_registered"`
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
