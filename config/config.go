package config

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// Config holds all poller configuration.
type Config struct {
	PollerToken    string `json:"poller_token"`    // AP_POLLER_TOKEN (required)
	APIURL         string `json:"api_url"`         // AP_API_URL (required)
	PollInterval   int    `json:"poll_interval"`   // AP_POLL_INTERVAL — seconds between monitor fetches (default: 60)
	MaxConcurrency int    `json:"max_concurrency"` // AP_MAX_CONCURRENCY — max concurrent checks (default: 50)
	BatchSize      int    `json:"batch_size"`      // AP_BATCH_SIZE — max results per batch POST (default: 100)
	BatchInterval  int    `json:"batch_interval"`  // AP_BATCH_INTERVAL — seconds between batch submissions (default: 10)
	HealthPort     int    `json:"health_port"`     // AP_HEALTH_PORT — local health endpoint port (default: 8089)
	LogLevel       string `json:"log_level"`       // AP_LOG_LEVEL — "debug", "info", "warn", "error" (default: "info")
	TLSInsecure    bool   `json:"tls_insecure"`    // AP_TLS_INSECURE — skip TLS verification for checks (default: false)
}

// DefaultConfig returns a Config with default values.
func DefaultConfig() *Config {
	return &Config{
		PollInterval:   60,
		MaxConcurrency: 50,
		BatchSize:      100,
		BatchInterval:  10,
		HealthPort:     8089,
		LogLevel:       "info",
		TLSInsecure:    false,
	}
}

// Load loads configuration with priority: env vars > config file > defaults.
func Load(configFile string) (*Config, error) {
	cfg := DefaultConfig()

	// Load from file if specified and exists
	if configFile != "" {
		data, err := os.ReadFile(configFile)
		if err == nil {
			if err := json.Unmarshal(data, cfg); err != nil {
				return nil, fmt.Errorf("failed to parse config file: %w", err)
			}
		}
	}

	// Override with environment variables
	if v := os.Getenv("AP_POLLER_TOKEN"); v != "" {
		cfg.PollerToken = v
	}
	if v := os.Getenv("AP_API_URL"); v != "" {
		cfg.APIURL = strings.TrimRight(v, "/")
	}
	if v := os.Getenv("AP_POLL_INTERVAL"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cfg.PollInterval = n
		}
	}
	if v := os.Getenv("AP_MAX_CONCURRENCY"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cfg.MaxConcurrency = n
		}
	}
	if v := os.Getenv("AP_BATCH_SIZE"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cfg.BatchSize = n
		}
	}
	if v := os.Getenv("AP_BATCH_INTERVAL"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cfg.BatchInterval = n
		}
	}
	if v := os.Getenv("AP_HEALTH_PORT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cfg.HealthPort = n
		}
	}
	if v := os.Getenv("AP_LOG_LEVEL"); v != "" {
		cfg.LogLevel = strings.ToLower(v)
	}
	if v := os.Getenv("AP_TLS_INSECURE"); v != "" {
		cfg.TLSInsecure = v == "true" || v == "1"
	}

	// Validate required fields
	if cfg.PollerToken == "" {
		return nil, fmt.Errorf("AP_POLLER_TOKEN is required")
	}
	if cfg.APIURL == "" {
		return nil, fmt.Errorf("AP_API_URL is required")
	}

	return cfg, nil
}
