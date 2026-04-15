package reporting

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/lssolutions-ie/lss-backup-cli/v2/internal/activitylog"
	"github.com/lssolutions-ie/lss-backup-cli/v2/internal/audit"
	"github.com/lssolutions-ie/lss-backup-cli/v2/internal/config"
)

const (
	reportTimeout  = 15 * time.Second
	reportEndpoint = "/api/v1/status"
)

// envelope is the outer HTTP request body sent to the management server.
// data is base64(nonce || AES-256-GCM ciphertext || tag) of the JSON NodeStatus.
type envelope struct {
	V    string `json:"v"`
	UID  string `json:"uid"`
	Data string `json:"data"`
}

// httpReporter sends encrypted NodeStatus payloads to the management server.
// Config is re-read from disk on every send so settings changes apply without
// a daemon restart.
type httpReporter struct {
	rootDir string
	logsDir string
}

// NewReporter returns a NoOpReporter when reporting is disabled or not
// fully configured, and an httpReporter when ready.
func NewReporter(cfg config.AppConfig, rootDir, logsDir string) Reporter {
	if !cfg.Enabled || cfg.ServerURL == "" || cfg.NodeID == "" || len(cfg.PSKKey) != 128 {
		return NoOpReporter{}
	}
	return &httpReporter{rootDir: rootDir, logsDir: logsDir}
}

// Report fires an encrypted payload to the management server in a goroutine.
// It is fire-and-forget: errors are written to activity.log only.
func (r *httpReporter) Report(status NodeStatus) {
	go r.send(status)
}

// ReportSync sends the status synchronously, blocking until complete.
// Returns the server response so callers can check tunnel_key_registered.
func (r *httpReporter) ReportSync(status NodeStatus) ReportResponse {
	return r.doSend(status)
}

func (r *httpReporter) send(status NodeStatus) {
	r.doSend(status)
}

func (r *httpReporter) doSend(status NodeStatus) ReportResponse {
	// Re-read config fresh so any settings update is picked up immediately.
	cfg, err := config.LoadAppConfig(r.rootDir)
	if err != nil || !cfg.Enabled || cfg.ServerURL == "" || cfg.NodeID == "" || len(cfg.PSKKey) != 128 {
		return ReportResponse{}
	}

	// Resolve node name: config field → hostname fallback.
	if cfg.NodeHostname != "" {
		status.NodeName = cfg.NodeHostname
	} else if status.NodeName == "" {
		status.NodeName, _ = os.Hostname()
	}

	plaintext, err := json.Marshal(status)
	if err != nil {
		r.warn("marshal payload: " + err.Error())
		return ReportResponse{}
	}

	encrypted, err := encryptAESGCM(cfg.PSKKey, plaintext)
	if err != nil {
		r.warn("encrypt payload: " + err.Error())
		return ReportResponse{}
	}

	env := envelope{V: "3", UID: cfg.NodeID, Data: encrypted}
	body, err := json.Marshal(env)
	if err != nil {
		r.warn("marshal envelope: " + err.Error())
		return ReportResponse{}
	}

	ctx, cancel := context.WithTimeout(context.Background(), reportTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.ServerURL+reportEndpoint, bytes.NewReader(body))
	if err != nil {
		r.warn("build request: " + err.Error())
		return ReportResponse{}
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		r.warn("send failed: " + err.Error())
		return ReportResponse{}
	}
	defer resp.Body.Close()

	// Read and parse the response body.
	respBody, readErr := io.ReadAll(resp.Body)
	if readErr != nil {
		r.warn(fmt.Sprintf("read response body: %v", readErr))
		return ReportResponse{}
	}

	var result ReportResponse
	if err := json.Unmarshal(respBody, &result); err != nil {
		r.warn(fmt.Sprintf("parse response body: %v (body: %.200s)", err, string(respBody)))
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg := fmt.Sprintf("server returned %s", resp.Status)
		if resp.StatusCode == 400 {
			msg += " — possible clock drift; verify system time is correct (NTP)"
		}
		// Include response body for debugging (truncated).
		if len(respBody) > 0 {
			body := string(respBody)
			if len(body) > 500 {
				body = body[:500] + "..."
			}
			msg += " — " + body
		}
		r.warn(msg)
		// Don't ack on non-2xx — server may not have persisted.
		return result
	}

	// Trim the local audit queue up to the seq the server persisted.
	// Safe with audit_ack_seq=0 (no-op).
	if q := audit.Q(); q != nil && result.AuditAckSeq > 0 {
		if err := q.AckUpTo(result.AuditAckSeq); err != nil {
			r.warn("audit queue ack: " + err.Error())
		}
	}

	// Record any reconcile-stats requests from the server. The daemon
	// drains this set before the next heartbeat, runs restic stats per
	// job, and attaches repo_size_bytes to the outgoing payload.
	if len(result.ReconcileRepoStats) > 0 {
		RequestReconcile(result.ReconcileRepoStats)
	}

	return result
}

func (r *httpReporter) warn(msg string) {
	activitylog.Log(r.logsDir, "[WARN] reporter: "+msg)
}

// encryptAESGCM encrypts plaintext using AES-256-GCM with a key derived from
// pskKey via SHA-256. Returns base64(nonce || ciphertext || tag).
func encryptAESGCM(pskKey string, plaintext []byte) (string, error) {
	keyBytes := sha256.Sum256([]byte(pskKey))

	block, err := aes.NewCipher(keyBytes[:])
	if err != nil {
		return "", fmt.Errorf("create cipher: %w", err)
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("create gcm: %w", err)
	}

	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("generate nonce: %w", err)
	}

	// Seal appends ciphertext+tag to nonce giving: nonce || ciphertext || tag.
	ciphertext := gcm.Seal(nonce, nonce, plaintext, nil)
	return base64.StdEncoding.EncodeToString(ciphertext), nil
}
