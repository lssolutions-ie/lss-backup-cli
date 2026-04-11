package config

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// AppConfig holds node-level settings persisted to {RootDir}/config.toml.
// The file is written with mode 0o600 because it contains the PSK key.
type AppConfig struct {
	Enabled   bool
	ServerURL string
	NodeID    string
	NodeHostname  string
	PSKKey    string // 128 printable ASCII chars; never log this value
}

const appConfigFile = "config.toml"

// AppConfigPath returns the path to the node-level config file.
func AppConfigPath(rootDir string) string {
	return filepath.Join(rootDir, appConfigFile)
}

// LoadAppConfig reads {rootDir}/config.toml. Returns a zero-value AppConfig
// (reporting disabled) if the file does not exist.
func LoadAppConfig(rootDir string) (AppConfig, error) {
	path := AppConfigPath(rootDir)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return AppConfig{}, nil
		}
		return AppConfig{}, fmt.Errorf("read app config: %w", err)
	}
	return parseAppConfigTOML(string(data))
}

// SaveAppConfig writes cfg to {rootDir}/config.toml with mode 0o600.
func SaveAppConfig(rootDir string, cfg AppConfig) error {
	path := AppConfigPath(rootDir)
	content := renderAppConfigTOML(cfg)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		return fmt.Errorf("write app config: %w", err)
	}
	return nil
}

func renderAppConfigTOML(cfg AppConfig) string {
	var sb strings.Builder
	sb.WriteString("[reporting]\n")
	if cfg.Enabled {
		sb.WriteString("enabled = true\n")
	} else {
		sb.WriteString("enabled = false\n")
	}
	sb.WriteString(fmt.Sprintf("server_url = %s\n", strconv.Quote(cfg.ServerURL)))
	sb.WriteString(fmt.Sprintf("node_id = %s\n", strconv.Quote(cfg.NodeID)))
	sb.WriteString(fmt.Sprintf("node_hostname = %s\n", strconv.Quote(cfg.NodeHostname)))
	sb.WriteString(fmt.Sprintf("psk_key = %s\n", strconv.Quote(cfg.PSKKey)))
	return sb.String()
}

func parseAppConfigTOML(raw string) (AppConfig, error) {
	var cfg AppConfig
	section := ""

	scanner := bufio.NewScanner(strings.NewReader(raw))
	for lineNumber := 1; scanner.Scan(); lineNumber++ {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section = strings.TrimSpace(line[1 : len(line)-1])
			continue
		}

		key, value, ok := strings.Cut(line, "=")
		if !ok {
			return AppConfig{}, fmt.Errorf("app config line %d: expected key = value", lineNumber)
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)

		if section != "reporting" {
			continue // ignore unknown sections
		}

		switch key {
		case "enabled":
			b, err := parseBool(value)
			if err != nil {
				return AppConfig{}, fmt.Errorf("app config line %d: parse enabled: %w", lineNumber, err)
			}
			cfg.Enabled = b
		case "server_url":
			cfg.ServerURL = parseString(value)
		case "node_id":
			cfg.NodeID = parseString(value)
		case "node_hostname":
			cfg.NodeHostname = parseString(value)
		case "psk_key":
			cfg.PSKKey = parseString(value)
		}
	}

	return cfg, scanner.Err()
}
