// Command mqttprobe is the test client used by scripts/scenarios. It exists so
// the scenarios need nothing installed beyond Go, and so they exercise the same
// paho settings the simulator and ingest use.
//
// Exit code 0 means the action succeeded; any other code means it did not. The
// negative scenario asserts the failures.
package main

import (
	"crypto/tls"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/url"
	"os"
	"strings"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"

	"github.com/tynext/tynext-poc/internal/mqttx"
)

func main() {
	var (
		action   = flag.String("action", "connect", "connect | publish | subscribe | verify-cert")
		broker   = flag.String("url", os.Getenv(mqttx.EnvURL), "broker URL (default $RAIN_MQTT_URL)")
		user     = flag.String("user", os.Getenv(mqttx.EnvUser), "username (default $RAIN_MQTT_USER)")
		pass     = flag.String("pass", os.Getenv(mqttx.EnvPass), "password (default $RAIN_MQTT_PASS)")
		caFile   = flag.String("ca", os.Getenv(mqttx.EnvCA), "CA bundle (default $RAIN_MQTT_CA)")
		clientID = flag.String("client-id", "", "clientId (default: the username)")
		topic    = flag.String("topic", "", "topic to publish to or subscribe on")
		payload  = flag.String("payload", `{"probe":true}`, "publish payload")
		qos      = flag.Int("qos", 1, "QoS")
		retain   = flag.Bool("retain", false, "publish retained")
		timeout  = flag.Duration("timeout", 10*time.Second, "how long to wait for the action")
		insecure = flag.Bool("insecure", false, "skip certificate verification (negative test only)")
		noCreds  = flag.Bool("no-credentials", false, "connect without username/password")
	)
	flag.Parse()

	cfg := mqttx.Config{
		URL:                *broker,
		Username:           *user,
		Password:           *pass,
		CAFile:             *caFile,
		ClientID:           *clientID,
		CleanSession:       true,
		KeepAlive:          5 * time.Second,
		InsecureSkipVerify: *insecure,
	}
	if *noCreds {
		cfg.Username, cfg.Password = "", ""
	}
	if cfg.ClientID == "" {
		cfg.ClientID = defaultClientID(cfg.Username)
	}

	if err := run(*action, cfg, *topic, *payload, byte(*qos), *retain, *timeout); err != nil {
		fmt.Fprintf(os.Stderr, "mqttprobe %s: %v\n", *action, err)
		os.Exit(1)
	}
}

func defaultClientID(username string) string {
	if username != "" {
		return username
	}
	return fmt.Sprintf("probe-%d", time.Now().UnixNano()%1_000_000)
}

func run(action string, cfg mqttx.Config, topic, payload string, qos byte, retain bool, timeout time.Duration) error {
	switch action {
	case "verify-cert":
		return verifyCert(cfg, timeout)
	case "connect":
		client, err := connect(cfg, timeout)
		if err != nil {
			return err
		}
		defer client.Disconnect(250)
		fmt.Printf("connected as %q (clientId %q) to %s\n", cfg.Username, cfg.ClientID, cfg.URL)
		return nil
	case "publish":
		return publish(cfg, topic, payload, qos, retain, timeout)
	case "subscribe":
		return subscribe(cfg, topic, qos, timeout)
	default:
		return fmt.Errorf("unknown action %q", action)
	}
}

// connectRetry is disabled for the probe: a scenario that expects a refusal
// must see it immediately instead of watching paho retry forever.
func connect(cfg mqttx.Config, timeout time.Duration) (mqtt.Client, error) {
	opts, err := cfg.Options()
	if err != nil {
		return nil, err
	}
	opts.SetConnectRetry(false)
	opts.SetAutoReconnect(false)
	opts.SetConnectTimeout(timeout)

	client := mqtt.NewClient(opts)
	token := client.Connect()
	if !token.WaitTimeout(timeout) {
		return nil, fmt.Errorf("CONNECT timed out after %s", timeout)
	}
	if err := token.Error(); err != nil {
		return nil, fmt.Errorf("CONNECT refused: %w", err)
	}
	return client, nil
}

func publish(cfg mqttx.Config, topic, payload string, qos byte, retain bool, timeout time.Duration) error {
	if topic == "" {
		return errors.New("-topic is required")
	}
	client, err := connect(cfg, timeout)
	if err != nil {
		return err
	}
	defer client.Disconnect(250)

	token := client.Publish(topic, qos, retain, payload)
	if !token.WaitTimeout(timeout) {
		// With authorization.deny_action = disconnect, a forbidden publish gets
		// no PUBACK and the broker drops the connection instead.
		if !client.IsConnectionOpen() {
			return fmt.Errorf("publish to %q was refused: broker closed the connection", topic)
		}
		return fmt.Errorf("publish to %q got no PUBACK within %s", topic, timeout)
	}
	if err := token.Error(); err != nil {
		return fmt.Errorf("publish to %q failed: %w", topic, err)
	}
	// Give the broker a moment to drop us if the ACL said no.
	time.Sleep(500 * time.Millisecond)
	if !client.IsConnectionOpen() {
		return fmt.Errorf("publish to %q was refused: broker closed the connection", topic)
	}
	fmt.Printf("published %d bytes to %q (qos %d, retain %t)\n", len(payload), topic, qos, retain)
	return nil
}

func subscribe(cfg mqttx.Config, topic string, qos byte, timeout time.Duration) error {
	if topic == "" {
		return errors.New("-topic is required")
	}
	received := make(chan mqtt.Message, 16)
	client, err := connect(cfg, timeout)
	if err != nil {
		return err
	}
	defer client.Disconnect(250)

	token := client.Subscribe(topic, qos, func(_ mqtt.Client, m mqtt.Message) {
		select {
		case received <- m:
		default:
		}
	})
	if !token.WaitTimeout(timeout) {
		if !client.IsConnectionOpen() {
			return fmt.Errorf("subscribe to %q was refused: broker closed the connection", topic)
		}
		return fmt.Errorf("subscribe to %q got no SUBACK within %s", topic, timeout)
	}
	if err := token.Error(); err != nil {
		return fmt.Errorf("subscribe to %q failed: %w", topic, err)
	}
	if sub, ok := token.(*mqtt.SubscribeToken); ok {
		for filter, granted := range sub.Result() {
			if granted == 0x80 {
				return fmt.Errorf("subscribe to %q was refused: broker returned SUBACK 0x80", filter)
			}
		}
	}
	time.Sleep(500 * time.Millisecond)
	if !client.IsConnectionOpen() {
		return fmt.Errorf("subscribe to %q was refused: broker closed the connection", topic)
	}

	fmt.Printf("subscribed to %q (qos %d)\n", topic, qos)
	select {
	case m := <-received:
		fmt.Printf("received on %q: %s\n", m.Topic(), truncate(string(m.Payload()), 200))
	case <-time.After(timeout):
		fmt.Printf("no message within %s (subscription itself was accepted)\n", timeout)
	}
	return nil
}

// verifyCert does a plain TLS handshake and checks the broker chain against the
// CA we hand to the OT team, then prints what the client actually saw.
func verifyCert(cfg mqttx.Config, timeout time.Duration) error {
	u, err := url.Parse(cfg.URL)
	if err != nil {
		return fmt.Errorf("parse %s: %w", cfg.URL, err)
	}
	if !cfg.IsTLS() {
		return fmt.Errorf("%s is not a TLS URL", cfg.URL)
	}

	tlsCfg, err := cfg.TLSConfig()
	if err != nil {
		return err
	}
	dialer := &net.Dialer{Timeout: timeout}
	conn, err := tls.DialWithDialer(dialer, "tcp", u.Host, tlsCfg)
	if err != nil {
		return fmt.Errorf("TLS handshake with %s failed: %w", u.Host, err)
	}
	defer conn.Close()

	state := conn.ConnectionState()
	if len(state.PeerCertificates) == 0 {
		return errors.New("broker presented no certificate")
	}
	leaf := state.PeerCertificates[0]

	fmt.Printf("tls version    : %s\n", tlsVersion(state.Version))
	fmt.Printf("cipher suite   : %s\n", tls.CipherSuiteName(state.CipherSuite))
	fmt.Printf("subject        : %s\n", leaf.Subject)
	fmt.Printf("issuer         : %s\n", leaf.Issuer)
	fmt.Printf("san dns        : %s\n", strings.Join(leaf.DNSNames, ", "))
	fmt.Printf("san ip         : %s\n", joinIPs(leaf.IPAddresses))
	fmt.Printf("not before     : %s\n", leaf.NotBefore.UTC().Format(time.RFC3339))
	fmt.Printf("not after      : %s\n", leaf.NotAfter.UTC().Format(time.RFC3339))
	fmt.Printf("verified chains: %d\n", len(state.VerifiedChains))

	if cfg.InsecureSkipVerify {
		fmt.Println("warning        : verification was skipped (-insecure)")
		return nil
	}
	if len(state.VerifiedChains) == 0 {
		return errors.New("certificate was not verified against the configured CA")
	}
	return nil
}

func tlsVersion(v uint16) string {
	switch v {
	case tls.VersionTLS12:
		return "TLS 1.2"
	case tls.VersionTLS13:
		return "TLS 1.3"
	default:
		return fmt.Sprintf("0x%04x", v)
	}
}

func joinIPs(ips []net.IP) string {
	out := make([]string, 0, len(ips))
	for _, ip := range ips {
		out = append(out, ip.String())
	}
	return strings.Join(out, ", ")
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
