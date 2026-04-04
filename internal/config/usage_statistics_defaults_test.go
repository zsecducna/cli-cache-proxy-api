package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadConfigOptional_UsageStatisticsEnabledDefaultsTrue(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(configPath, []byte("port: 8317\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	cfg, err := LoadConfigOptional(configPath, false)
	if err != nil {
		t.Fatalf("LoadConfigOptional() error = %v", err)
	}
	if !cfg.UsageStatisticsEnabled {
		t.Fatal("UsageStatisticsEnabled = false, want true when the key is omitted")
	}
}

func TestLoadConfigOptional_UsageStatisticsEnabledAllowsExplicitFalse(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(configPath, []byte("port: 8317\nusage-statistics-enabled: false\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	cfg, err := LoadConfigOptional(configPath, false)
	if err != nil {
		t.Fatalf("LoadConfigOptional() error = %v", err)
	}
	if cfg.UsageStatisticsEnabled {
		t.Fatal("UsageStatisticsEnabled = true, want false when explicitly configured")
	}
}
