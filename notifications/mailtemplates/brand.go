package mailtemplates

import (
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
)

const (
	// DefaultAccent is the Fil accent (accent.solid): the button fill and the light-mode link colour.
	DefaultAccent = "#1d62e9"
	// DefaultAccentDark is the Fil dark-mode link colour (accent.fg) that goes with DefaultAccent.
	DefaultAccentDark = "#6a99f1"
	// darkCard is the card colour in dark mode, the background the dark accent text must read on.
	darkCard = "#171717"
	// minContrast is the WCAG AA ratio for body text.
	minContrast = 4.5
	white       = "#ffffff"
	black       = "#000000"
)

var hexColorRgx = regexp.MustCompile(`^#[0-9a-f]{6}$`)

// Brand holds the per-deployment values every email shares. ExecTemplate and ExecPlain expose
// it to the templates as .Brand.
type Brand struct {
	// Accent is the button fill and the light-mode link colour, as #rrggbb.
	Accent string
	// AccentDark is the dark-mode link colour, derived from Accent to keep 4.5:1 on the dark card.
	AccentDark string
	// AccentLabel is the button label colour: #ffffff or #000000, whichever reads on Accent.
	AccentLabel string
	// AssetsURL is the base URL of the Vocdoni logo PNGs, without a trailing slash.
	AssetsURL string
	// ServerURL is the public URL of this API, without a trailing slash; organization logos are
	// shown only when they are stored under it (see orgLogoURL).
	ServerURL string
	// SiteURL is the Vocdoni website linked from the footer.
	SiteURL string
	// SupportEmail is the support address shown in the footer.
	SupportEmail string
}

// DefaultBrand returns the brand of a deployment with no mail configuration, serving its logos
// from http://localhost:8080 (the serverURL flag default).
func DefaultBrand() Brand {
	return Brand{
		Accent:       DefaultAccent,
		AccentDark:   DefaultAccentDark,
		AccentLabel:  white,
		AssetsURL:    "http://localhost:8080/mail/assets",
		ServerURL:    "http://localhost:8080",
		SiteURL:      "https://vocdoni.io",
		SupportEmail: "support@vocdoni.org",
	}
}

// NewBrand returns the brand for the given accent colour (#rrggbb), public server URL and logo
// base URL. An empty assetsURL means {serverURL}/mail/assets. The dark link colour and the button
// label colour are derived from the accent.
func NewBrand(accent, serverURL, assetsURL string) (Brand, error) {
	accent = strings.ToLower(strings.TrimSpace(accent))
	if !hexColorRgx.MatchString(accent) {
		return Brand{}, fmt.Errorf("invalid mail accent colour %q, want #rrggbb", accent)
	}
	serverURL = strings.TrimRight(strings.TrimSpace(serverURL), "/")
	if serverURL == "" {
		return Brand{}, fmt.Errorf("the server URL is required")
	}
	assetsURL = strings.TrimRight(strings.TrimSpace(assetsURL), "/")
	if assetsURL == "" {
		assetsURL = serverURL + "/mail/assets"
	}
	b := DefaultBrand()
	b.Accent = accent
	b.AccentDark = darkAccent(accent)
	b.AccentLabel = labelColor(accent)
	b.ServerURL = serverURL
	b.AssetsURL = assetsURL
	return b, nil
}

// validate reports whether b holds usable colours and URLs.
func (b Brand) validate() error {
	for _, c := range []string{b.Accent, b.AccentDark, b.AccentLabel} {
		if !hexColorRgx.MatchString(c) {
			return fmt.Errorf("invalid brand colour %q", c)
		}
	}
	if b.AssetsURL == "" || b.ServerURL == "" || b.SiteURL == "" || b.SupportEmail == "" {
		return fmt.Errorf("incomplete brand: %+v", b)
	}
	return nil
}

// darkAccent lightens accent towards white in 1% steps until it reaches 4.5:1 on the dark card.
// The default accent keeps the Fil token value.
func darkAccent(accent string) string {
	if accent == DefaultAccent {
		return DefaultAccentDark
	}
	r, g, b := rgb(accent)
	for step := 0; step <= 100; step++ {
		t := float64(step) / 100
		c := hexColor(mix(r, t), mix(g, t), mix(b, t))
		if contrast(c, darkCard) >= minContrast {
			return c
		}
	}
	return white
}

// labelColor returns white when it reaches 4.5:1 on the accent, black otherwise (one of the two
// always does).
func labelColor(accent string) string {
	if contrast(white, accent) >= minContrast {
		return white
	}
	return black
}

func mix(c uint8, t float64) uint8 {
	return uint8(math.Round(float64(c) + (255-float64(c))*t))
}

func rgb(hex string) (r, g, b uint8) {
	v, _ := strconv.ParseUint(strings.TrimPrefix(hex, "#"), 16, 32)
	return uint8(v >> 16), uint8(v >> 8), uint8(v)
}

func hexColor(r, g, b uint8) string {
	return fmt.Sprintf("#%02x%02x%02x", r, g, b)
}

// contrast is the WCAG 2 contrast ratio between two #rrggbb colours.
func contrast(a, b string) float64 {
	la, lb := luminance(a), luminance(b)
	return (math.Max(la, lb) + 0.05) / (math.Min(la, lb) + 0.05)
}

func luminance(hex string) float64 {
	r, g, b := rgb(hex)
	channel := func(c uint8) float64 {
		v := float64(c) / 255
		if v <= 0.04045 {
			return v / 12.92
		}
		return math.Pow((v+0.055)/1.055, 2.4)
	}
	return 0.2126*channel(r) + 0.7152*channel(g) + 0.0722*channel(b)
}
