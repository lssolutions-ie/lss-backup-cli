// Package tunnel manages a persistent reverse SSH tunnel from the node to the
// management server. The tunnel forwards a dynamically allocated remote port
// back to the node's local SSH server (port 22), allowing the management
// server's web terminal to reach the node without inbound connectivity.
package tunnel

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
)

const (
	reconnectDelay = 15 * time.Second
	keyFileName    = "tunnel_key"
	pubKeyFileName = "tunnel_key.pub"
	localSSHTarget = "localhost:22"
)

// Status holds the current tunnel state for heartbeat reporting.
type Status struct {
	Port      int    `json:"port"`
	PublicKey string `json:"public_key"`
	Connected bool   `json:"connected"`
}

// Manager holds the tunnel state and provides status for heartbeat reporting.
type Manager struct {
	mu        sync.RWMutex
	port      int
	connected bool
	publicKey string
	stateDir  string
}

// NewManager creates a tunnel manager. Call Run() to start the tunnel.
func NewManager(stateDir string) *Manager {
	return &Manager{stateDir: stateDir}
}

// Status returns the current tunnel state for inclusion in heartbeat payloads.
func (m *Manager) Status() Status {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return Status{
		Port:      m.port,
		PublicKey: m.publicKey,
		Connected: m.connected,
	}
}

// Run starts the reverse tunnel and blocks until ctx is cancelled.
// It automatically reconnects on failure.
func (m *Manager) Run(ctx context.Context, sshHost string, sshPort int, nodeID string) {
	// Load or generate the key pair.
	signer, pubKeyStr, err := m.loadOrGenerateKey()
	if err != nil {
		log.Printf("Tunnel: failed to load/generate key: %v", err)
		return
	}

	m.mu.Lock()
	m.publicKey = pubKeyStr
	m.mu.Unlock()

	config := &ssh.ClientConfig{
		User: "lss-tunnel",
		Auth: []ssh.AuthMethod{
			ssh.PublicKeys(signer),
		},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(), // TODO: known_hosts
		Timeout:         15 * time.Second,
	}

	addr := fmt.Sprintf("%s:%d", sshHost, sshPort)

	for {
		select {
		case <-ctx.Done():
			m.setConnected(false, 0)
			return
		default:
		}

		m.connect(ctx, addr, config, nodeID)

		// Wait before reconnecting.
		select {
		case <-ctx.Done():
			return
		case <-time.After(reconnectDelay):
			log.Println("Tunnel: reconnecting...")
		}
	}
}

func (m *Manager) connect(ctx context.Context, addr string, config *ssh.ClientConfig, nodeID string) {
	client, err := ssh.Dial("tcp", addr, config)
	if err != nil {
		log.Printf("Tunnel: dial failed: %v", err)
		m.setConnected(false, 0)
		return
	}
	defer client.Close()

	// Request a reverse port forward with port 0 (server picks).
	listener, err := client.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		log.Printf("Tunnel: reverse forward failed: %v", err)
		m.setConnected(false, 0)
		return
	}
	defer listener.Close()

	// Extract the allocated port.
	_, portStr, _ := net.SplitHostPort(listener.Addr().String())
	port := 0
	fmt.Sscanf(portStr, "%d", &port)

	log.Printf("Tunnel: connected, remote port %d → %s", port, localSSHTarget)
	m.setConnected(true, port)

	// Accept connections and forward them to local SSH.
	doneCh := make(chan struct{})
	go func() {
		defer close(doneCh)
		for {
			remote, err := listener.Accept()
			if err != nil {
				return
			}
			go m.forward(remote)
		}
	}()

	// Wait for context cancellation or connection drop.
	select {
	case <-ctx.Done():
	case <-doneCh:
		log.Println("Tunnel: connection lost")
	}

	m.setConnected(false, 0)
}

func (m *Manager) forward(remote net.Conn) {
	defer remote.Close()

	local, err := net.DialTimeout("tcp", localSSHTarget, 5*time.Second)
	if err != nil {
		return
	}
	defer local.Close()

	done := make(chan struct{}, 2)
	go func() { io.Copy(local, remote); done <- struct{}{} }()  //nolint:errcheck
	go func() { io.Copy(remote, local); done <- struct{}{} }()  //nolint:errcheck
	<-done
}

func (m *Manager) setConnected(connected bool, port int) {
	m.mu.Lock()
	m.connected = connected
	m.port = port
	m.mu.Unlock()
}

func (m *Manager) loadOrGenerateKey() (ssh.Signer, string, error) {
	keyPath := filepath.Join(m.stateDir, keyFileName)
	pubPath := filepath.Join(m.stateDir, pubKeyFileName)

	// Try to load existing key.
	keyData, err := os.ReadFile(keyPath)
	if err == nil {
		signer, err := ssh.ParsePrivateKey(keyData)
		if err == nil {
			pubData, _ := os.ReadFile(pubPath)
			return signer, string(pubData), nil
		}
	}

	// Generate new ed25519 key pair.
	pubKey, privKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, "", fmt.Errorf("generate key: %w", err)
	}

	// Marshal private key to PEM.
	privBytes, err := ssh.MarshalPrivateKey(privKey, "")
	if err != nil {
		return nil, "", fmt.Errorf("marshal private key: %w", err)
	}
	privPEM := pem.EncodeToMemory(privBytes)

	// Marshal public key to authorized_keys format.
	sshPub, err := ssh.NewPublicKey(pubKey)
	if err != nil {
		return nil, "", fmt.Errorf("marshal public key: %w", err)
	}
	pubStr := string(ssh.MarshalAuthorizedKey(sshPub))

	// Write to disk.
	if err := os.WriteFile(keyPath, privPEM, 0o600); err != nil {
		return nil, "", fmt.Errorf("write private key: %w", err)
	}
	if err := os.WriteFile(pubPath, []byte(pubStr), 0o644); err != nil {
		return nil, "", fmt.Errorf("write public key: %w", err)
	}

	signer, err := ssh.ParsePrivateKey(privPEM)
	if err != nil {
		return nil, "", fmt.Errorf("parse generated key: %w", err)
	}

	log.Printf("Tunnel: generated new ed25519 key pair")
	return signer, pubStr, nil
}
