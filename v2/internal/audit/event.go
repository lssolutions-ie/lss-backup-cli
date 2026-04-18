package audit

// Event categories — closed enum matching the server contract for v2.3.0.
// Adding a new one requires a coordinated protocol agreement with the server
// so it can render/color new categories in the audit UI.
const (
	CategoryDaemonStarted             = "daemon_started"
	CategoryDaemonStopped             = "daemon_stopped"
	CategoryJobCreated                = "job_created"
	CategoryJobModified               = "job_modified"
	CategoryJobDeleted                = "job_deleted"
	CategoryScheduleChanged           = "schedule_changed"
	CategoryRetentionChanged          = "retention_changed"
	CategoryNotificationsChanged      = "notifications_changed"
	CategoryRunFailed                 = "run_failed"
	CategoryRunPermissionDenied       = "run_permission_denied"
	CategoryRestoreStarted            = "restore_started"
	CategoryRestoreCompleted          = "restore_completed"
	CategoryRestoreFailed             = "restore_failed"
	CategorySSHCredentialsConfigured  = "ssh_credentials_configured"
	CategoryMgmtConsoleConfigured     = "mgmt_console_configured"
	CategoryMgmtConsoleCleared        = "mgmt_console_cleared"
	CategoryUpdateInstalled           = "update_installed"
	CategoryTunnelConnected           = "tunnel_connected"
	CategoryTunnelDisconnected        = "tunnel_disconnected"
	CategoryDRRestore                 = "dr_restore"
	CategoryCredentialsRegenerated    = "credentials_regenerated"
)

// Event severity levels.
const (
	SeverityInfo     = "info"
	SeverityWarn     = "warn"
	SeverityCritical = "critical"
)

// Actor prefixes.
const (
	ActorSystem = "system"
)

// Event is the wire shape for audit events. Matches the server contract
// exactly — field names/tags must not change without a protocol bump.
//
// TS is Unix seconds UTC (no timezone ambiguity). Seq is a per-node monotonic
// counter that never rewinds or reuses a value. Details is an optional
// map of structured key/value context; the server soft-caps the serialized
// size at 8 KB (client truncates at 2 KB).
//
// HMAC (v2.5+): HMAC-SHA256 chain over (prev_hmac || canonical_json(event))
// keyed with the node PSK. Server verifies incrementally; chain break →
// CRITICAL audit row + ack freeze. Empty when PSK is not configured or
// the node hasn't been upgraded to v2.5+.
type Event struct {
	Seq      uint64            `json:"seq"`
	TS       int64             `json:"ts"`
	Category string            `json:"category"`
	Severity string            `json:"severity"`
	Actor    string            `json:"actor"`
	Message  string            `json:"message"`
	Details  map[string]string `json:"details,omitempty"`
	HMAC     string            `json:"hmac,omitempty"`
}

// Maximum characters for Event.Message before client-side truncation.
// Server doesn't reject longer, but we truncate to keep payloads predictable.
const MessageMaxChars = 500

// Maximum serialized size (in bytes) of the Details map before trimming.
// Safety net: server hard-caps at 8 KB; we aim to stay well under.
const DetailsMaxBytes = 2048
