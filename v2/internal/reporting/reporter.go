package reporting

// Reporter sends the current node status snapshot to a management server.
// All implementations must be fire-and-forget: a failed report must never
// block a backup job or propagate an error to the caller.
type Reporter interface {
	Report(status NodeStatus)
	// ReportSync sends the status synchronously, blocking until complete.
	// Used at daemon startup to ensure the server has the tunnel key
	// before the tunnel attempts to connect.
	ReportSync(status NodeStatus)
}

// NoOpReporter is used when reporting is disabled.
type NoOpReporter struct{}

func (NoOpReporter) Report(status NodeStatus)     {}
func (NoOpReporter) ReportSync(status NodeStatus) {}
