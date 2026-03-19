package installmanifest

import (
	"encoding/json"
	"fmt"
	"os"
)

type Dependency struct {
	Name              string `json:"name"`
	Manager           string `json:"manager"`
	PackageID         string `json:"package_id"`
	PreviouslyPresent bool   `json:"previously_present"`
	InstalledByProgram bool  `json:"installed_by_program"`
}

type Manifest struct {
	OS             string       `json:"os"`
	InstalledAt    string       `json:"installed_at"`
	PackageManager string       `json:"package_manager"`
	BinaryPath     string       `json:"binary_path"`
	ConfigDir      string       `json:"config_dir"`
	JobsDir        string       `json:"jobs_dir"`
	LogsDir        string       `json:"logs_dir"`
	StateDir       string       `json:"state_dir"`
	Dependencies   []Dependency `json:"dependencies"`
}

func Load(path string) (Manifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Manifest{}, fmt.Errorf("read %s: %w", path, err)
	}

	var manifest Manifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return Manifest{}, fmt.Errorf("parse %s: %w", path, err)
	}

	return manifest, nil
}
