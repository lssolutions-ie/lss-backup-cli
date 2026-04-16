// Package dr implements server-controlled disaster recovery for the CLI's
// own configuration. The management server pushes S3 credentials + restic
// password via the heartbeat response; this package schedules periodic
// backups of all job configs, secrets, tunnel keys, and audit state to
// the designated S3 repo.
package dr

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Config is the DR configuration pushed by the management server in the
// heartbeat response. The CLI caches it locally (encrypted) and uses it
// to schedule + execute config backups to S3.
type Config struct {
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

// RepoURL returns the full restic S3 repository URL for this node.
func (c Config) RepoURL() string {
	return fmt.Sprintf("s3:%s/%s/%s", c.S3Endpoint, c.S3Bucket, c.NodeFolder)
}

// Status is reported back to the server in every heartbeat so the
// dashboard can render the shield icon.
type Status struct {
	Configured   bool       `json:"configured"`
	LastBackupAt *time.Time `json:"last_backup_at,omitempty"`
	StatusText   string     `json:"status,omitempty"` // "success" or "failure"
	Error        string     `json:"error,omitempty"`
	SnapshotCount int       `json:"snapshot_count,omitempty"`
}

const (
	configFile = "dr_config.enc"
	statusFile = "dr_status.json"
)

// Manager holds the DR state and coordinates scheduled backups.
type Manager struct {
	stateDir string
	psk      string // node PSK for encrypting cached config
	mu       sync.Mutex
	config   *Config
	status   Status
}

// NewManager creates a DR manager. Loads cached config + status from disk
// if present. psk is the node PSK used to encrypt/decrypt the cached
// dr_config (same key as heartbeat AES-256-GCM).
func NewManager(stateDir, psk string) *Manager {
	m := &Manager{stateDir: stateDir, psk: psk}
	if cfg, err := m.loadConfig(); err == nil {
		m.config = cfg
		m.status.Configured = true
	}
	if st, err := m.loadStatus(); err == nil {
		m.status = st
	}
	return m
}

// UpdateConfig is called when a new dr_config arrives in a heartbeat
// response. Persists to disk (encrypted) and returns true if the config
// changed (caller should trigger an immediate backup).
func (m *Manager) UpdateConfig(cfg Config) (changed bool, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.config != nil && *m.config == cfg {
		return false, nil
	}
	if err := m.saveConfig(cfg); err != nil {
		return false, err
	}
	m.config = &cfg
	m.status.Configured = cfg.Enabled
	return true, nil
}

// GetConfig returns the current DR config, or nil if not configured.
func (m *Manager) GetConfig() *Config {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.config == nil {
		return nil
	}
	c := *m.config
	return &c
}

// GetStatus returns the current DR status for heartbeat reporting.
func (m *Manager) GetStatus() Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.status
}

// RecordSuccess records a successful DR backup.
func (m *Manager) RecordSuccess(snapshotCount int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now().UTC()
	m.status.LastBackupAt = &now
	m.status.StatusText = "success"
	m.status.Error = ""
	m.status.SnapshotCount = snapshotCount
	m.status.Configured = true
	_ = m.saveStatus(m.status)
}

// RecordFailure records a failed DR backup.
func (m *Manager) RecordFailure(errMsg string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now().UTC()
	m.status.LastBackupAt = &now
	m.status.StatusText = "failure"
	m.status.Error = errMsg
	m.status.Configured = true
	_ = m.saveStatus(m.status)
}

// IsDue returns true if enough time has passed since the last backup
// that a new one should run.
func (m *Manager) IsDue() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.config == nil || !m.config.Enabled {
		return false
	}
	if m.status.LastBackupAt == nil {
		return true // never ran
	}
	interval := time.Duration(m.config.IntervalHours) * time.Hour
	if interval <= 0 {
		interval = 24 * time.Hour
	}
	return time.Since(*m.status.LastBackupAt) >= interval
}

// --- encrypted config persistence ---

func (m *Manager) saveConfig(cfg Config) error {
	data, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	encrypted, err := encryptBytes(m.psk, data)
	if err != nil {
		return err
	}
	path := filepath.Join(m.stateDir, configFile)
	return atomicWrite(path, encrypted, 0o600)
}

func (m *Manager) loadConfig() (*Config, error) {
	path := filepath.Join(m.stateDir, configFile)
	encrypted, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	data, err := decryptBytes(m.psk, encrypted)
	if err != nil {
		return nil, err
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func (m *Manager) saveStatus(st Status) error {
	data, err := json.Marshal(st)
	if err != nil {
		return err
	}
	return atomicWrite(filepath.Join(m.stateDir, statusFile), data, 0o644)
}

func (m *Manager) loadStatus() (Status, error) {
	data, err := os.ReadFile(filepath.Join(m.stateDir, statusFile))
	if err != nil {
		return Status{}, err
	}
	var st Status
	if err := json.Unmarshal(data, &st); err != nil {
		return Status{}, err
	}
	return st, nil
}

// --- AES-256-GCM encrypt/decrypt (same scheme as heartbeat payloads) ---

func encryptBytes(psk string, plaintext []byte) ([]byte, error) {
	if psk == "" {
		return nil, errors.New("dr: no PSK for encryption")
	}
	key := sha256.Sum256([]byte(psk))
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	return gcm.Seal(nonce, nonce, plaintext, nil), nil
}

func decryptBytes(psk string, ciphertext []byte) ([]byte, error) {
	if psk == "" {
		return nil, errors.New("dr: no PSK for decryption")
	}
	key := sha256.Sum256([]byte(psk))
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	if len(ciphertext) < gcm.NonceSize() {
		return nil, errors.New("dr: ciphertext too short")
	}
	nonce := ciphertext[:gcm.NonceSize()]
	return gcm.Open(nil, nonce, ciphertext[gcm.NonceSize():], nil)
}

func atomicWrite(path string, data []byte, perm os.FileMode) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, perm); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
