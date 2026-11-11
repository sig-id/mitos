package messaging

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
)

type Grant struct {
	Project string `json:"project"`
	Mailbox string `json:"mailbox"`
	Kind    string `json:"kind"`
	Type    string `json:"type"`
}
type Peer struct {
	ID        string  `json:"id"`
	PublicKey string  `json:"public_key"`
	Grants    []Grant `json:"grants"`
}
type Config struct {
	Broker        string `json:"broker"`
	Username      string `json:"username,omitempty"`
	PasswordEnv   string `json:"password_env,omitempty"`
	CAFile        string `json:"ca_file,omitempty"`
	CertFile      string `json:"cert_file,omitempty"`
	KeyFile       string `json:"key_file,omitempty"`
	SessionExpiry uint32 `json:"session_expiry_seconds,omitempty"`
	MaxAttempts   int    `json:"max_attempts,omitempty"`
	Peers         []Peer `json:"peers"`
}

func LoadConfig(path string) (Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return Config{}, err
	}
	if len(b) > MaxPacket {
		return Config{}, errors.New("MQTT configuration too large")
	}
	var c Config
	if err = Decode(b, &c); err != nil {
		return c, err
	}
	_, _, err = c.validate()
	return c, err
}
func (c *Config) validate() (*url.URL, *tls.Config, error) {
	u, err := url.Parse(c.Broker)
	if err != nil || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return nil, nil, errors.New("invalid MQTT broker URL; credentials belong in configuration")
	}
	if u.Scheme != "mqtt" && u.Scheme != "mqtts" && u.Scheme != "tls" {
		return nil, nil, errors.New("use mqtt:// for loopback or mqtts:// for TLS")
	}
	if u.Scheme == "mqtt" {
		ip := net.ParseIP(u.Hostname())
		if ip == nil || !ip.IsLoopback() {
			return nil, nil, errors.New("plaintext MQTT requires a literal loopback address")
		}
	}
	tc := &tls.Config{MinVersion: tls.VersionTLS12}
	if c.CAFile != "" {
		b, e := os.ReadFile(c.CAFile)
		if e != nil {
			return nil, nil, e
		}
		roots, e := x509.SystemCertPool()
		if e != nil {
			roots = x509.NewCertPool()
		}
		if !roots.AppendCertsFromPEM(b) {
			return nil, nil, errors.New("invalid MQTT CA")
		}
		tc.RootCAs = roots
	}
	if (c.CertFile == "") != (c.KeyFile == "") {
		return nil, nil, errors.New("MQTT client certificate and key must be provided together")
	}
	if c.CertFile != "" {
		cert, e := tls.LoadX509KeyPair(c.CertFile, c.KeyFile)
		if e != nil {
			return nil, nil, e
		}
		tc.Certificates = []tls.Certificate{cert}
	}
	if u.Scheme == "mqtts" {
		u.Scheme = "tls"
	}
	if c.SessionExpiry == 0 {
		c.SessionExpiry = 86400
	}
	if c.SessionExpiry > 7*86400 {
		return nil, nil, errors.New("MQTT session expiry exceeds seven days")
	}
	if c.MaxAttempts == 0 {
		c.MaxAttempts = 32
	}
	if c.MaxAttempts < 1 || c.MaxAttempts > 100 {
		return nil, nil, errors.New("MQTT max_attempts must be 1–100")
	}
	seen := map[string]bool{}
	for _, p := range c.Peers {
		key, e := base64.StdEncoding.DecodeString(p.PublicKey)
		if !ValidID(p.ID) || seen[p.ID] || e != nil || len(key) != ed25519.PublicKeySize {
			return nil, nil, errors.New("invalid or duplicate MQTT peer")
		}
		seen[p.ID] = true
		for _, g := range p.Grants {
			if !ValidID(g.Project) || !ValidID(g.Mailbox) || (g.Kind != "note" && g.Kind != "command" && g.Kind != "event") || g.Type == "" {
				return nil, nil, errors.New("peer grants must name exact project, mailbox, kind and type")
			}
		}
	}
	return u, tc, nil
}

// Identity is separate from transport credentials, so a broker cannot impersonate
// another home server. Never place the private key in message or journal records.
func LoadIdentity(dir string) (ed25519.PrivateKey, error) {
	path := filepath.Join(dir, "mqtt-ed25519.key")
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		_, key, e := ed25519.GenerateKey(rand.Reader)
		if e != nil {
			return nil, e
		}
		f, e := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if e != nil {
			return nil, e
		}
		_, e = f.Write(key)
		if e == nil {
			e = f.Sync()
		}
		ce := f.Close()
		if e != nil {
			return nil, e
		}
		if ce != nil {
			return nil, ce
		}
		d, e := os.Open(dir)
		if e != nil {
			return nil, e
		}
		e = d.Sync()
		d.Close()
		if e != nil {
			return nil, e
		}
		b = key
	} else if err != nil {
		return nil, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if info.Mode().Perm()&0077 != 0 || len(b) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("invalid MQTT identity file or permissions: %s", path)
	}
	key := ed25519.NewKeyFromSeed(b[:ed25519.SeedSize])
	if string(key) != string(b) {
		return nil, errors.New("corrupt MQTT signing key")
	}
	return key, nil
}
