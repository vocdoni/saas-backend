package main

import (
	"fmt"
	"os"
	"strings"

	flag "github.com/spf13/pflag"
	"github.com/spf13/viper"
	"github.com/vocdoni/saas-backend/certauth"
)

// defineCertAuthFlags defines the --certAuth* flags (VOCDONI_CERTAUTH* variables). Certificate
// sign-in is off unless certAuthMode is set, and then none of the others is read.
func defineCertAuthFlags() {
	flag.String("certAuthMode", "", "certificate sign-in: empty (off), origin or cloudflare")
	flag.String("certAuthHost", "", "certificate auth domain: attempts run on <nonce>.<host>, the probe on probe.<host>")
	flag.String("certAuthListenAddr", certauth.DefaultListenAddr, "bind address of the certificate auth listener")
	flag.Int("certAuthPublicPort", certauth.DefaultPublicPort, "public port of the auth listener URLs (omitted when 443)")
	flag.String("certAuthTLSCert", "", "PEM file of the auth listener certificate, covering *.<certAuthHost>")
	flag.String("certAuthTLSKey", "", "PEM file of the auth listener certificate key")
	flag.String("certAuthProviders", "fnmt,idcat", "comma list of certificate providers (fnmt, idcat)")
	flag.String("certAuthAllowedOrigins", "", "comma list of the app origins a returnUrl, the CORS and the probe allow")
	flag.String("certAuthDefaultReturn", "", "app URL an expired certificate attempt is sent back to")
	flag.Int("certAuthCreatesPerMinute", certauth.DefaultCreatesPerMinute,
		"certificate attempts per client IP and process per minute (<= 0 = unlimited)")
	flag.Bool("certAuthProxyProtocol", false, "accept PROXY protocol headers from certAuthProxyFrontIPs (origin mode)")
	flag.String("certAuthProxyFrontIPs", "", "comma list of the CIDRs allowed to send PROXY protocol headers")
	flag.String("certAuthCFOriginPullCA", "", "PEM file of the Cloudflare Authenticated Origin Pulls CA (cloudflare mode)")
	flag.String("certAuthCFAllowedIPs", "", "comma list of the CIDRs the auth listener accepts in cloudflare mode")
	flag.Bool("certAuthCloudflareAcknowledged", false,
		"cloudflare mode opt-in: set only after the blockers in the spec section 3b are verified and"+
			" the AOP CA is a per-zone or per-hostname certificate, never Cloudflare's global one")
	flag.Bool("certAuthTestProvider", false, "enable the synthetic certificate provider (dev and e2e only)")
	flag.String("certAuthTestProviderCA", "", "PEM file of the synthetic certificate provider CA")
	flag.String("certAuthTestProviderAllowedAPIs", "",
		"comma list of extra --vocdoniApi hosts the synthetic provider allows (stage must be listed here explicitly)")
}

// certAuthConfig reads the --certAuth* configuration and its PEM files. certauth.New validates it.
func certAuthConfig(v *viper.Viper) (certauth.Config, error) {
	cfg := certauth.Config{
		Mode:                    v.GetString("certAuthMode"),
		Host:                    v.GetString("certAuthHost"),
		ListenAddr:              v.GetString("certAuthListenAddr"),
		PublicPort:              v.GetInt("certAuthPublicPort"),
		Providers:               splitList(v.GetString("certAuthProviders")),
		AllowedOrigins:          splitList(v.GetString("certAuthAllowedOrigins")),
		DefaultReturn:           v.GetString("certAuthDefaultReturn"),
		CreatesPerMinute:        v.GetInt("certAuthCreatesPerMinute"),
		ProxyProtocol:           v.GetBool("certAuthProxyProtocol"),
		ProxyFrontCIDRs:         splitList(v.GetString("certAuthProxyFrontIPs")),
		CFAllowedCIDRs:          splitList(v.GetString("certAuthCFAllowedIPs")),
		CFAcknowledged:          v.GetBool("certAuthCloudflareAcknowledged"),
		TestProvider:            v.GetBool("certAuthTestProvider"),
		TestProviderAllowedAPIs: splitList(v.GetString("certAuthTestProviderAllowedAPIs")),
	}
	var err error
	for _, f := range []struct {
		key string
		dst *[]byte
	}{
		{"certAuthTLSCert", &cfg.TLSCertPEM},
		{"certAuthTLSKey", &cfg.TLSKeyPEM},
		{"certAuthCFOriginPullCA", &cfg.CFOriginPullCAPEM},
		{"certAuthTestProviderCA", &cfg.TestProviderCAPEM},
	} {
		path := v.GetString(f.key)
		if path == "" {
			continue
		}
		if *f.dst, err = os.ReadFile(path); err != nil {
			return certauth.Config{}, fmt.Errorf("reading %s: %w", f.key, err)
		}
	}
	return cfg, nil
}

// splitList splits a comma list, trimming spaces and dropping empty items. Viper splits a string
// slice from an environment variable on spaces, not commas, so the lists are plain strings.
func splitList(s string) []string {
	var out []string
	for item := range strings.SplitSeq(s, ",") {
		if item = strings.TrimSpace(item); item != "" {
			out = append(out, item)
		}
	}
	return out
}
