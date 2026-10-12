// Package mqttx wraps the paho client with the connection settings this PoC
// shares between the simulator (stand-in TF6701) and ingest: MQTT 3.1.1 over
// TLS 1.2+, username/password, and an optional client certificate.
package mqttx

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
)

// Config describes one MQTT connection.
type Config struct {
	// URL is the broker address, e.g. tls://mqtt-poc.example.test:8883.
	URL string
	// Username and Password are the broker credentials.
	Username string
	Password string
	// CAFile verifies the broker certificate. Empty means system roots.
	CAFile string
	// ClientCert and ClientKey are only needed if the broker is set to
	// mutual TLS. TBC: that decision has not been made yet.
	ClientCert string
	ClientKey  string

	// ClientID must stay stable so a session survives a restart.
	ClientID string
	// CleanSession false keeps queued QoS 1 messages across reconnects.
	CleanSession bool
	// KeepAlive drives how fast the broker declares the client dead and
	// publishes the will. 5 s keeps LWT detection under the 10 s criterion.
	KeepAlive time.Duration

	// InsecureSkipVerify disables certificate verification. Only the negative
	// test scenario sets it, to show what a misconfigured client looks like.
	InsecureSkipVerify bool
}

// Env variable names, all prefixed RAIN_.
const (
	EnvURL        = "RAIN_MQTT_URL"
	EnvUser       = "RAIN_MQTT_USER"
	EnvPass       = "RAIN_MQTT_PASS"
	EnvCA         = "RAIN_MQTT_CA"
	EnvClientCert = "RAIN_MQTT_CLIENT_CERT"
	EnvClientKey  = "RAIN_MQTT_CLIENT_KEY"
)

// ConfigFromEnv reads the RAIN_MQTT_* variables.
func ConfigFromEnv() Config {
	return Config{
		URL:        os.Getenv(EnvURL),
		Username:   os.Getenv(EnvUser),
		Password:   os.Getenv(EnvPass),
		CAFile:     os.Getenv(EnvCA),
		ClientCert: os.Getenv(EnvClientCert),
		ClientKey:  os.Getenv(EnvClientKey),
		KeepAlive:  5 * time.Second,
	}
}

// Validate reports whether the config can be used to dial.
func (c Config) Validate() error {
	if c.URL == "" {
		return fmt.Errorf("%s is empty", EnvURL)
	}
	u, err := url.Parse(c.URL)
	if err != nil {
		return fmt.Errorf("%s %q is not a URL: %w", EnvURL, c.URL, err)
	}
	switch u.Scheme {
	case "tls", "ssl", "mqtts", "tcp", "mqtt", "ws", "wss":
	default:
		return fmt.Errorf("%s has unsupported scheme %q", EnvURL, u.Scheme)
	}
	if c.ClientID == "" {
		return errors.New("clientID is empty")
	}
	if (c.ClientCert == "") != (c.ClientKey == "") {
		return fmt.Errorf("%s and %s must be set together", EnvClientCert, EnvClientKey)
	}
	return nil
}

// IsTLS reports whether the URL asks for an encrypted connection.
func (c Config) IsTLS() bool {
	return strings.HasPrefix(c.URL, "tls://") ||
		strings.HasPrefix(c.URL, "ssl://") ||
		strings.HasPrefix(c.URL, "mqtts://") ||
		strings.HasPrefix(c.URL, "wss://")
}

// ServerName returns the host the certificate is verified against.
func (c Config) ServerName() string {
	u, err := url.Parse(c.URL)
	if err != nil {
		return ""
	}
	return u.Hostname()
}

// TLSConfig builds the TLS settings: TLS 1.2 floor, the internal CA, and the
// client certificate when one is configured.
func (c Config) TLSConfig() (*tls.Config, error) {
	cfg := &tls.Config{
		MinVersion:         tls.VersionTLS12,
		ServerName:         c.ServerName(),
		InsecureSkipVerify: c.InsecureSkipVerify, //nolint:gosec // only the negative scenario sets this
	}

	if c.CAFile != "" {
		pem, err := os.ReadFile(c.CAFile)
		if err != nil {
			return nil, fmt.Errorf("read CA %s: %w", c.CAFile, err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("CA %s contains no usable certificate", c.CAFile)
		}
		cfg.RootCAs = pool
	}

	if c.ClientCert != "" {
		pair, err := tls.LoadX509KeyPair(c.ClientCert, c.ClientKey)
		if err != nil {
			return nil, fmt.Errorf("load client certificate: %w", err)
		}
		cfg.Certificates = []tls.Certificate{pair}
	}

	return cfg, nil
}

// Options turns the config into paho options. Protocol version is pinned to
// MQTT 3.1.1 to match what TF6701 speaks.
func (c Config) Options() (*mqtt.ClientOptions, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}

	opts := mqtt.NewClientOptions().
		AddBroker(c.URL).
		SetClientID(c.ClientID).
		SetUsername(c.Username).
		SetPassword(c.Password).
		SetProtocolVersion(4). // MQTT 3.1.1
		SetCleanSession(c.CleanSession).
		SetKeepAlive(c.KeepAlive).
		SetPingTimeout(c.KeepAlive).
		SetConnectTimeout(10 * time.Second).
		SetWriteTimeout(10 * time.Second).
		SetOrderMatters(true).
		SetAutoReconnect(true).
		SetConnectRetry(true).
		SetConnectRetryInterval(2 * time.Second).
		SetMaxReconnectInterval(30 * time.Second)

	if c.IsTLS() {
		tlsCfg, err := c.TLSConfig()
		if err != nil {
			return nil, err
		}
		opts.SetTLSConfig(tlsCfg)
	}

	return opts, nil
}
