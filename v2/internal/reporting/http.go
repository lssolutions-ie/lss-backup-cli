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
	"github.com/lssolutions-ie/lss-backup-cli/v2/internal/config"
)

const (
	reportTimeout  = 15 * time.Second
	reportEndpoint = "/api/v1/node/status"
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
	if !cfg.Enabled || cfg.ServerURL == "" || cfg.UserID == "" || len(cfg.PSKKey) != 128 {
		return NoOpReporter{}
	}
	return &httpReporter{rootDir: rootDir, logsDir: logsDir}
}

// Report fires an encrypted payload to the management server in a goroutine.
// It is fire-and-forget: errors are written to activity.log only.
func (r *httpReporter) Report(status NodeStatus) {
	go r.send(status)
}

func (r *httpReporter) send(status NodeStatus) {
	// Re-read config fresh so any settings update is picked up immediately.
	cfg, err := config.LoadAppConfig(r.rootDir)
	if err != nil || !cfg.Enabled || cfg.ServerURL == "" || cfg.UserID == "" || len(cfg.PSKKey) != 128 {
		return
	}

	// Resolve node name: config field → hostname fallback.
	if cfg.NodeName != "" {
		status.NodeName = cfg.NodeName
	} else if status.NodeName == "" {
		status.NodeName, _ = os.Hostname()
	}

	plaintext, err := json.Marshal(status)
	if err != nil {
		r.warn("marshal payload: " + err.Error())
		return
	}

	encrypted, err := encryptAESGCM(cfg.PSKKey, plaintext)
	if err != nil {
		r.warn("encrypt payload: " + err.Error())
		return
	}

	env := envelope{V: "1", UID: cfg.UserID, Data: encrypted}
	body, err := json.Marshal(env)
	if err != nil {
		r.warn("marshal envelope: " + err.Error())
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), reportTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.ServerURL+reportEndpoint, bytes.NewReader(body))
	if err != nil {
		r.warn("build request: " + err.Error())
		return
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		r.warn("send failed: " + err.Error())
		return
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body) //nolint:errcheck

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		r.warn(fmt.Sprintf("server returned %s", resp.Status))
	}
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
