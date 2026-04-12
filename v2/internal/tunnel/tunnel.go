// Package tunnel manages a persistent reverse SSH tunnel from the node to the
// management server over WebSocket. The tunnel connects to wss://<server>/ws/ssh-tunnel
// which proxies to the server's local sshd. The node then establishes a reverse
// port forward so the server's web terminal can reach the node's SSH without
// inbound connectivity.
package tunnel

import (
	"context"
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"golang.org/x/crypto/ssh"
)

const (
	reconnectDelay = 15 * time.Second
	keyFileName    = "tunnel_key"
	pubKeyFileName = "tunnel_key.pub"
	localSSHTarget = "localhost:22"
	tunnelWSPath   = "/ws/ssh-tunnel"
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

// NewManager creates a tunnel manager and loads (or generates) the key pair
// eagerly so Status().PublicKey is available before Run() starts.
func NewManager(stateDir string) *Manager {
	m := &Manager{stateDir: stateDir}
	if _, pubKeyStr, err := m.loadOrGenerateKey(); err == nil {
		m.publicKey = pubKeyStr
	}
	return m
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
// It automatically reconnects on failure. serverURL is the management
// server's HTTPS URL (e.g. "https://lssbackup.lssolutions.ie").
func (m *Manager) Run(ctx context.Context, serverURL, nodeID, pskKey string) {
	// Load or generate the key pair.
	signer, pubKeyStr, err := m.loadOrGenerateKey()
	if err != nil {
		log.Printf("Tunnel: failed to load/generate key: %v", err)
		return
	}

	m.mu.Lock()
	m.publicKey = pubKeyStr
	m.mu.Unlock()

	// Derive WebSocket URL from server URL.
	wsURL := deriveWSURL(serverURL)

	sshConfig := &ssh.ClientConfig{
		User: "lss-tunnel",
		Auth: []ssh.AuthMethod{
			ssh.PublicKeys(signer),
		},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(), // server sshd reached via trusted WebSocket
		Timeout:         15 * time.Second,
	}

	for {
		select {
		case <-ctx.Done():
			m.setConnected(false, 0)
			return
		default:
		}

		m.connect(ctx, wsURL, nodeID, pskKey, sshConfig)

		// Wait before reconnecting.
		select {
		case <-ctx.Done():
			return
		case <-time.After(reconnectDelay):
			log.Println("Tunnel: reconnecting...")
		}
	}
}

func (m *Manager) connect(ctx context.Context, wsURL, nodeID, pskKey string, sshConfig *ssh.ClientConfig) {
	// Build HMAC auth headers.
	ts := fmt.Sprintf("%d", time.Now().Unix())
	mac := computeHMAC(pskKey, "ssh-tunnel:"+nodeID+":"+ts)

	headers := http.Header{}
	headers.Set("X-LSS-UID", nodeID)
	headers.Set("X-LSS-TS", ts)
	headers.Set("X-LSS-HMAC", mac)

	dialer := websocket.Dialer{
		HandshakeTimeout: 15 * time.Second,
	}

	ws, _, err := dialer.DialContext(ctx, wsURL, headers)
	if err != nil {
		log.Printf("Tunnel: WebSocket dial failed: %v", err)
		m.setConnected(false, 0)
		return
	}
	defer ws.Close()

	// Wrap the WebSocket as a net.Conn for the SSH client.
	conn := newWSConn(ws)

	// Establish SSH session over the WebSocket.
	sshConn, chans, reqs, err := ssh.NewClientConn(conn, wsURL, sshConfig)
	if err != nil {
		log.Printf("Tunnel: SSH handshake failed: %v", err)
		m.setConnected(false, 0)
		return
	}
	client := ssh.NewClient(sshConn, chans, reqs)
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

	log.Printf("Tunnel: connected via WebSocket, remote port %d → %s", port, localSSHTarget)
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
			return signer, strings.TrimSpace(string(pubData)), nil
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
	pubStr := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(sshPub)))

	// Write to disk.
	if err := os.WriteFile(keyPath, privPEM, 0o600); err != nil {
		return nil, "", fmt.Errorf("write private key: %w", err)
	}
	if err := os.WriteFile(pubPath, []byte(pubStr+"\n"), 0o644); err != nil {
		return nil, "", fmt.Errorf("write public key: %w", err)
	}

	signer, err := ssh.ParsePrivateKey(privPEM)
	if err != nil {
		return nil, "", fmt.Errorf("parse generated key: %w", err)
	}

	log.Printf("Tunnel: generated new ed25519 key pair")
	return signer, pubStr, nil
}

// deriveWSURL converts an HTTPS server URL to a WSS tunnel URL.
func deriveWSURL(serverURL string) string {
	u := strings.TrimRight(serverURL, "/")
	if strings.HasPrefix(u, "https://") {
		return "wss://" + u[len("https://"):] + tunnelWSPath
	}
	if strings.HasPrefix(u, "http://") {
		return "ws://" + u[len("http://"):] + tunnelWSPath
	}
	return "wss://" + u + tunnelWSPath
}

// computeHMAC returns the lowercase hex of HMAC-SHA256(psk, message).
func computeHMAC(psk, message string) string {
	mac := hmac.New(sha256.New, []byte(psk))
	mac.Write([]byte(message))
	return hex.EncodeToString(mac.Sum(nil))
}

// wsConn wraps a gorilla/websocket.Conn as a net.Conn so it can be used
// with ssh.NewClientConn. All frames use BinaryMessage.
type wsConn struct {
	ws *websocket.Conn
	r  io.Reader
}

func newWSConn(ws *websocket.Conn) *wsConn {
	return &wsConn{ws: ws}
}

func (c *wsConn) Read(p []byte) (int, error) {
	for {
		if c.r == nil {
			_, r, err := c.ws.NextReader()
			if err != nil {
				return 0, err
			}
			c.r = r
		}
		n, err := c.r.Read(p)
		if err == io.EOF {
			c.r = nil
			if n > 0 {
				return n, nil
			}
			continue
		}
		return n, err
	}
}

func (c *wsConn) Write(p []byte) (int, error) {
	err := c.ws.WriteMessage(websocket.BinaryMessage, p)
	if err != nil {
		return 0, err
	}
	return len(p), nil
}

func (c *wsConn) Close() error                       { return c.ws.Close() }
func (c *wsConn) LocalAddr() net.Addr                { return c.ws.LocalAddr() }
func (c *wsConn) RemoteAddr() net.Addr               { return c.ws.RemoteAddr() }
func (c *wsConn) SetDeadline(t time.Time) error      { return nil }
func (c *wsConn) SetReadDeadline(t time.Time) error  { return c.ws.SetReadDeadline(t) }
func (c *wsConn) SetWriteDeadline(t time.Time) error { return c.ws.SetWriteDeadline(t) }
