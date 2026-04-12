// Package hwinfo gathers basic hardware and network information for
// inclusion in heartbeat reports to the management server.
package hwinfo

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"
)

// Public IP cache — refreshed every publicIPRefreshInterval calls to Collect().
var (
	cachedPublicIP     string
	publicIPCallCount  int
	publicIPMu         sync.Mutex
	publicIPRefreshInterval = 12 // ~1 hour at 5-min heartbeat intervals
)

// Info holds the hardware and network snapshot for a node.
type Info struct {
	OS       string `json:"os"`        // "linux", "darwin", "windows"
	Arch     string `json:"arch"`      // "amd64", "arm64"
	CPUs     int    `json:"cpus"`      // number of logical CPUs
	Hostname string `json:"hostname"`
	RAM      uint64 `json:"ram_bytes"` // total physical RAM in bytes (0 if unavailable)
	Storage  []Disk `json:"storage,omitempty"`
	LANIP    string `json:"lan_ip,omitempty"`
	PublicIP string `json:"public_ip,omitempty"`
}

// Disk holds information about a mounted filesystem.
type Disk struct {
	Path       string `json:"path"`
	TotalBytes uint64 `json:"total_bytes"`
	FreeBytes  uint64 `json:"free_bytes"`
	UsedBytes  uint64 `json:"used_bytes"`
}

// Collect gathers hardware and network info. It is designed to be called
// periodically (e.g. on each heartbeat) and is best-effort — any field
// that cannot be determined is left at its zero value.
func Collect() Info {
	info := Info{
		OS:   runtime.GOOS,
		Arch: runtime.GOARCH,
		CPUs: runtime.NumCPU(),
	}

	info.Hostname, _ = os.Hostname()
	info.RAM = totalRAM()
	info.Storage = diskUsage()
	info.LANIP = lanIP()
	info.PublicIP = cachedPublicIPFetch()

	return info
}

// lanIP returns the preferred outbound LAN IP address.
func lanIP() string {
	conn, err := net.DialTimeout("udp", "8.8.8.8:80", 2*time.Second)
	if err != nil {
		return ""
	}
	defer conn.Close()
	addr := conn.LocalAddr().(*net.UDPAddr)
	return addr.IP.String()
}

// cachedPublicIPFetch returns the cached public IP, refreshing it every
// publicIPRefreshInterval calls (about once per hour at 5-min heartbeats).
func cachedPublicIPFetch() string {
	publicIPMu.Lock()
	defer publicIPMu.Unlock()

	publicIPCallCount++
	if cachedPublicIP == "" || publicIPCallCount >= publicIPRefreshInterval {
		publicIPCallCount = 0
		cachedPublicIP = fetchPublicIP()
	}
	return cachedPublicIP
}

// fetchPublicIP fetches the public IP from a lightweight API.
// 5-second timeout to avoid blocking the heartbeat.
func fetchPublicIP() string {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Try multiple providers in case one is down.
	providers := []string{
		"https://api.ipify.org",
		"https://ifconfig.me/ip",
		"https://icanhazip.com",
	}

	for _, url := range providers {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			continue
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			continue
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 64))
		resp.Body.Close()
		ip := strings.TrimSpace(string(body))
		if ip != "" && net.ParseIP(ip) != nil {
			return ip
		}
	}
	return ""
}

// formatBytes returns a human-readable string for a byte count.
func formatBytes(b uint64) string {
	const (
		gb = 1024 * 1024 * 1024
		mb = 1024 * 1024
	)
	switch {
	case b >= gb:
		return fmt.Sprintf("%.1f GB", float64(b)/float64(gb))
	case b >= mb:
		return fmt.Sprintf("%.1f MB", float64(b)/float64(mb))
	default:
		return fmt.Sprintf("%d B", b)
	}
}
