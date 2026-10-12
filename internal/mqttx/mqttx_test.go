package mqttx_test

import (
	"crypto/tls"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/tynext/tynext-poc/internal/mqttx"
)

func baseConfig() mqttx.Config {
	return mqttx.Config{
		URL:       "tls://mqtt-poc.example.test:8883",
		Username:  "ems-pv-01",
		Password:  "secret",
		ClientID:  "ems-pv-01",
		KeepAlive: 5 * time.Second,
	}
}

func TestValidate(t *testing.T) {
	if err := baseConfig().Validate(); err != nil {
		t.Fatalf("a complete config should validate: %v", err)
	}

	bad := []struct {
		name   string
		mutate func(*mqttx.Config)
	}{
		{"no URL", func(c *mqttx.Config) { c.URL = "" }},
		{"no clientID", func(c *mqttx.Config) { c.ClientID = "" }},
		{"unsupported scheme", func(c *mqttx.Config) { c.URL = "http://broker:8883" }},
		{"client cert without key", func(c *mqttx.Config) { c.ClientCert = "a.crt" }},
		{"client key without cert", func(c *mqttx.Config) { c.ClientKey = "a.key" }},
	}
	for _, tc := range bad {
		t.Run(tc.name, func(t *testing.T) {
			cfg := baseConfig()
			tc.mutate(&cfg)
			if err := cfg.Validate(); err == nil {
				t.Error("config should have been rejected")
			}
		})
	}
}

func TestIsTLSAndServerName(t *testing.T) {
	cases := map[string]struct {
		isTLS bool
		host  string
	}{
		"tls://mqtt-poc.example.test:8883": {true, "mqtt-poc.example.test"},
		"ssl://broker:8883":                {true, "broker"},
		"mqtts://broker:8883":              {true, "broker"},
		"wss://broker:443/mqtt":            {true, "broker"},
		"tcp://127.0.0.1:1883":             {false, "127.0.0.1"},
		"mqtt://localhost:1883":            {false, "localhost"},
	}
	for rawURL, want := range cases {
		cfg := baseConfig()
		cfg.URL = rawURL
		if got := cfg.IsTLS(); got != want.isTLS {
			t.Errorf("%s: IsTLS = %t, want %t", rawURL, got, want.isTLS)
		}
		if got := cfg.ServerName(); got != want.host {
			t.Errorf("%s: ServerName = %q, want %q", rawURL, got, want.host)
		}
	}
}

// The server name must come from the URL, not be left empty: an empty
// ServerName with a CA pool would skip hostname checking.
func TestTLSConfigPinsServerNameAndFloor(t *testing.T) {
	cfg := baseConfig()
	tlsCfg, err := cfg.TLSConfig()
	if err != nil {
		t.Fatalf("TLSConfig: %v", err)
	}
	if tlsCfg.ServerName != "mqtt-poc.example.test" {
		t.Errorf("ServerName = %q, want the host from the URL", tlsCfg.ServerName)
	}
	if tlsCfg.MinVersion != tls.VersionTLS12 {
		t.Errorf("MinVersion = %x, want TLS 1.2", tlsCfg.MinVersion)
	}
	if tlsCfg.InsecureSkipVerify {
		t.Error("verification must be on unless explicitly disabled")
	}
	if tlsCfg.RootCAs != nil {
		t.Error("without a CA file the system roots should be used")
	}
}

func TestTLSConfigRejectsUnusableCA(t *testing.T) {
	dir := t.TempDir()
	junk := filepath.Join(dir, "ca.crt")
	if err := os.WriteFile(junk, []byte("this is not a certificate"), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg := baseConfig()
	cfg.CAFile = junk
	if _, err := cfg.TLSConfig(); err == nil {
		t.Error("a CA file with no certificate in it should be an error")
	}

	cfg.CAFile = filepath.Join(dir, "missing.crt")
	if _, err := cfg.TLSConfig(); err == nil {
		t.Error("a missing CA file should be an error")
	}
}

// The PoC pins MQTT 3.1.1 because that is what TF6701 speaks.
func TestOptionsMatchTF6701(t *testing.T) {
	cfg := baseConfig()
	cfg.CleanSession = false
	opts, err := cfg.Options()
	if err != nil {
		t.Fatalf("Options: %v", err)
	}
	if opts.ProtocolVersion != 4 {
		t.Errorf("ProtocolVersion = %d, want 4 (MQTT 3.1.1)", opts.ProtocolVersion)
	}
	if opts.CleanSession {
		t.Error("CleanSession should follow the config")
	}
	if opts.KeepAlive != int64(5*time.Second/time.Second) {
		t.Errorf("KeepAlive = %d, want 5 seconds", opts.KeepAlive)
	}
	if opts.ClientID != "ems-pv-01" {
		t.Errorf("ClientID = %q", opts.ClientID)
	}
	if opts.TLSConfig == nil || opts.TLSConfig.ServerName == "" {
		t.Error("a tls:// URL must produce a TLS config")
	}
}

func TestConfigFromEnv(t *testing.T) {
	t.Setenv(mqttx.EnvURL, "tls://broker:8883")
	t.Setenv(mqttx.EnvUser, "ems-pv-01")
	t.Setenv(mqttx.EnvPass, "secret")
	t.Setenv(mqttx.EnvCA, "/etc/ca.crt")
	t.Setenv(mqttx.EnvClientCert, "")
	t.Setenv(mqttx.EnvClientKey, "")

	cfg := mqttx.ConfigFromEnv()
	if cfg.URL != "tls://broker:8883" || cfg.Username != "ems-pv-01" || cfg.CAFile != "/etc/ca.crt" {
		t.Errorf("ConfigFromEnv = %+v", cfg)
	}
	if cfg.KeepAlive != 5*time.Second {
		t.Errorf("KeepAlive = %s, want the 5 s default that keeps LWT under 10 s", cfg.KeepAlive)
	}
}
