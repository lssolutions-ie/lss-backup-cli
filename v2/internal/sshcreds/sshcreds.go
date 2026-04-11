// Package sshcreds manages per-node SSH credentials for management server
// terminal access. It creates a dedicated OS user with sudo/admin rights
// and stores the credentials encrypted with an operator-supplied password.
package sshcreds

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
)

const (
	credsFile    = "ssh-credentials.enc"
	usernameLen  = 12
	passwordLen  = 36
	alphanumeric = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
)

// Credentials holds the SSH username and password for terminal access.
type Credentials struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// CredsPath returns the path to the encrypted credentials file.
func CredsPath(rootDir string) string {
	return filepath.Join(rootDir, credsFile)
}

// Exists returns true if encrypted credentials are already stored.
func Exists(rootDir string) bool {
	_, err := os.Stat(CredsPath(rootDir))
	return err == nil
}

// GenerateCredentials creates a random username (lss_ prefix + 12 alphanumeric)
// and password (36 alphanumeric, no special characters).
func GenerateCredentials() (Credentials, error) {
	user, err := randomString(usernameLen)
	if err != nil {
		return Credentials{}, fmt.Errorf("generate username: %w", err)
	}
	pass, err := randomString(passwordLen)
	if err != nil {
		return Credentials{}, fmt.Errorf("generate password: %w", err)
	}
	return Credentials{
		Username: "lss_" + user,
		Password: pass,
	}, nil
}

// Save encrypts credentials with the operator's password and writes to disk.
func Save(rootDir string, creds Credentials, encryptionPassword string) error {
	plaintext, err := json.Marshal(creds)
	if err != nil {
		return fmt.Errorf("marshal credentials: %w", err)
	}

	encrypted, err := encrypt(plaintext, encryptionPassword)
	if err != nil {
		return fmt.Errorf("encrypt credentials: %w", err)
	}

	path := CredsPath(rootDir)
	if err := os.WriteFile(path, []byte(encrypted), 0o600); err != nil {
		return fmt.Errorf("write credentials file: %w", err)
	}
	return nil
}

// Load decrypts and returns the stored credentials.
func Load(rootDir string, encryptionPassword string) (Credentials, error) {
	path := CredsPath(rootDir)
	data, err := os.ReadFile(path)
	if err != nil {
		return Credentials{}, fmt.Errorf("read credentials file: %w", err)
	}

	plaintext, err := decrypt(string(data), encryptionPassword)
	if err != nil {
		return Credentials{}, fmt.Errorf("decrypt credentials: wrong password or corrupted file")
	}

	var creds Credentials
	if err := json.Unmarshal(plaintext, &creds); err != nil {
		return Credentials{}, fmt.Errorf("parse credentials: %w", err)
	}
	return creds, nil
}

// Remove deletes the encrypted credentials file.
func Remove(rootDir string) {
	os.Remove(CredsPath(rootDir))
}

func randomString(length int) (string, error) {
	result := make([]byte, length)
	max := big.NewInt(int64(len(alphanumeric)))
	for i := range result {
		n, err := rand.Int(rand.Reader, max)
		if err != nil {
			return "", err
		}
		result[i] = alphanumeric[n.Int64()]
	}
	return string(result), nil
}

func encrypt(plaintext []byte, password string) (string, error) {
	key := sha256.Sum256([]byte(password))
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	ciphertext := gcm.Seal(nonce, nonce, plaintext, nil)
	return base64.StdEncoding.EncodeToString(ciphertext), nil
}

func decrypt(encoded string, password string) ([]byte, error) {
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, err
	}
	key := sha256.Sum256([]byte(password))
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	if len(data) < gcm.NonceSize() {
		return nil, fmt.Errorf("ciphertext too short")
	}
	nonce := data[:gcm.NonceSize()]
	ciphertext := data[gcm.NonceSize():]
	return gcm.Open(nil, nonce, ciphertext, nil)
}
