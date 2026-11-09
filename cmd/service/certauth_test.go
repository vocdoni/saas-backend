package main

import (
	"os"
	"path/filepath"
	"testing"

	qt "github.com/frankban/quicktest"
	flag "github.com/spf13/pflag"
	"github.com/spf13/viper"
)

// TestCertAuthConfig pins how the --certAuth* configuration is read: comma lists from the
// environment, the defaults, and the PEM files loaded by path.
func TestCertAuthConfig(t *testing.T) {
	c := qt.New(t)
	dir := t.TempDir()
	certFile := filepath.Join(dir, "cert.pem")
	c.Assert(os.WriteFile(certFile, []byte("CERT"), 0o600), qt.IsNil)
	t.Setenv("VOCDONI_CERTAUTHMODE", "origin")
	t.Setenv("VOCDONI_CERTAUTHHOST", "cert.auth-dev.vocdoni.io")
	t.Setenv("VOCDONI_CERTAUTHALLOWEDORIGINS", "https://app-dev.vocdoni.io, http://localhost:3000,")
	t.Setenv("VOCDONI_CERTAUTHTLSCERT", certFile)

	flags := flag.NewFlagSet("test", flag.ContinueOnError)
	saved := flag.CommandLine
	flag.CommandLine = flags
	defer func() { flag.CommandLine = saved }()
	defineCertAuthFlags()
	v := viper.New()
	c.Assert(initViper(v, flags), qt.IsNil)

	cfg, err := certAuthConfig(v)
	c.Assert(err, qt.IsNil)
	c.Assert(cfg.Mode, qt.Equals, "origin")
	c.Assert(cfg.Host, qt.Equals, "cert.auth-dev.vocdoni.io")
	c.Assert(cfg.AllowedOrigins, qt.DeepEquals, []string{"https://app-dev.vocdoni.io", "http://localhost:3000"})
	c.Assert(cfg.Providers, qt.DeepEquals, []string{"fnmt", "idcat"})
	c.Assert(cfg.ListenAddr, qt.Equals, ":8443")
	c.Assert(cfg.PublicPort, qt.Equals, 443)
	c.Assert(cfg.CreatesPerMinute, qt.Equals, 60)
	c.Assert(string(cfg.TLSCertPEM), qt.Equals, "CERT")
	c.Assert(cfg.TLSKeyPEM, qt.IsNil)

	t.Setenv("VOCDONI_CERTAUTHTLSKEY", filepath.Join(dir, "missing.pem"))
	_, err = certAuthConfig(v)
	c.Assert(err, qt.ErrorMatches, "reading certAuthTLSKey: .*")
}
