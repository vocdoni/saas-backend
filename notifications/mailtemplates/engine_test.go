package mailtemplates

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	qt "github.com/frankban/quicktest"
)

func TestMain(m *testing.M) {
	if err := Load(DefaultBrand()); err != nil {
		panic(err)
	}
	m.Run()
}

func TestParseMessage(t *testing.T) {
	c := qt.New(t)
	for _, tc := range []struct {
		in, html, text, err string
	}{
		{in: "Hello, {{.UserName}}.", html: "Hello, Ana &lt;3.", text: "Hello, Ana <3."},
		{
			in: "Import at *{{ .Organization }}* done", html: "Import at <strong>Org &amp; Co</strong> done",
			text: "Import at Org & Co done",
		},
		{in: `5 \* 3`, html: "5 * 3", text: "5 * 3"},
		{in: "l’adreça d’*{{.Organization}}*", html: "l’adreça d’<strong>Org &amp; Co</strong>", text: "l’adreça d’Org & Co"},
		{in: "*open", err: "unmatched *"},
		{in: "a ** b", err: "empty emphasis"},
		{in: "<b>bold</b>", err: "markup is not allowed"},
		{in: "{{if .X}}x{{end}}", err: "only {{.Field}} is allowed"},
		{in: "{{.a}}", err: "only {{.Field}} is allowed"},
		{in: "stray }} here", err: "stray }}"},
		{in: `\n`, err: "unknown escape"},
	} {
		m, err := parseMessage(tc.in)
		if tc.err != "" {
			c.Assert(err, qt.ErrorMatches, ".*"+regexpQuote(tc.err)+".*", qt.Commentf("%q", tc.in))
			continue
		}
		c.Assert(err, qt.IsNil, qt.Commentf("%q", tc.in))
		values := fieldValues(map[string]string{"UserName": "Ana <3", "Organization": "Org & Co"})
		html, err := m.render(asHTML, values)
		c.Assert(err, qt.IsNil)
		c.Assert(html, qt.Equals, tc.html)
		text, err := m.render(asText, values)
		c.Assert(err, qt.IsNil)
		c.Assert(text, qt.Equals, tc.text)
	}
}

func regexpQuote(s string) string {
	return strings.NewReplacer(`*`, `\*`, `{`, `\{`, `}`, `\}`, `.`, `\.`).Replace(s)
}

func TestTruncateAndFit(t *testing.T) {
	c := qt.New(t)
	c.Assert(Truncate("Gràcia", 6), qt.Equals, "Gràcia")
	c.Assert(Truncate("Col·legi d’Enginyeria", 9), qt.Equals, "Col·legi…")
	c.Assert(utf8.RuneCountInString(Truncate(strings.Repeat("ç", 300), 140)), qt.Equals, 140)
	c.Assert(utf8.ValidString(Truncate(strings.Repeat("€", 10), 5)), qt.IsTrue)

	// short values keep their length, the long one takes what is left
	fitted := fitValues([]string{"Bug", strings.Repeat("x", 100)}, 40)
	c.Assert(fitted[0], qt.Equals, "Bug")
	c.Assert(utf8.RuneCountInString(fitted[1]), qt.Equals, 37)
	c.Assert(fitted[1], qt.Matches, `x+…`)
	c.Assert(fitValues([]string{"a", "b"}, 10), qt.DeepEquals, []string{"a", "b"})
}

func TestFormatter(t *testing.T) {
	c := qt.New(t)
	st := loaded.Load()
	f := func(lang string) formatter {
		return formatter{loc: st.locales[lang], en: st.locales["en"], zone: st.zone}
	}
	for d, want := range map[time.Duration]string{
		0:                              "1 minute",
		30 * time.Second:               "1 minute",
		15 * time.Minute:               "15 minutes",
		5*time.Minute + 30*time.Second: "6 minutes",
		59*time.Minute + time.Second:   "1 hour",
		90 * time.Minute:               "2 hours",
		24 * time.Hour:                 "1 day",
		5 * 24 * time.Hour:             "5 days",
	} {
		got, err := f("en").duration(d)
		c.Assert(err, qt.IsNil)
		c.Assert(got, qt.Equals, want, qt.Commentf("%s", d))
	}
	got, err := f("ca").duration(15 * time.Minute)
	c.Assert(err, qt.IsNil)
	c.Assert(got, qt.Equals, "15 minuts")

	// dates are shown in Europe/Madrid: 23:30 UTC on 31 March is already 1 April there
	when := time.Date(2026, time.March, 31, 23, 30, 0, 0, time.UTC)
	got, err = f("ca").date(when)
	c.Assert(err, qt.IsNil)
	c.Assert(got, qt.Equals, "1 d’abril de 2026")
	got, err = f("en").date(when)
	c.Assert(err, qt.IsNil)
	c.Assert(got, qt.Equals, "April 1, 2026")

	for n, want := range map[int64]string{0: "0", 999: "999", 1248: "1,248", 1234567: "1,234,567", -3000: "-3,000"} {
		got, err := f("en").number(n)
		c.Assert(err, qt.IsNil)
		c.Assert(got, qt.Equals, want)
	}
	got, err = f("ca").value(2990)
	c.Assert(err, qt.IsNil)
	c.Assert(got, qt.Equals, "2.990")
}

func TestBrand(t *testing.T) {
	c := qt.New(t)
	def := DefaultBrand()
	c.Assert(def.validate(), qt.IsNil)
	c.Assert(def.AccentDark, qt.Equals, "#6a99f1")

	b, err := NewBrand("#1D62E9", "https://api.example.org/", "")
	c.Assert(err, qt.IsNil)
	c.Assert(b.Accent, qt.Equals, DefaultAccent)
	c.Assert(b.AccentDark, qt.Equals, DefaultAccentDark)
	c.Assert(b.AccentLabel, qt.Equals, "#ffffff")
	c.Assert(b.ServerURL, qt.Equals, "https://api.example.org")
	c.Assert(b.AssetsURL, qt.Equals, "https://api.example.org/mail/assets")

	for _, accent := range []string{"#166534", "#000000", "#ffcc00", "#7f7f7f", "#e11d48"} {
		b, err := NewBrand(accent, "https://api.example.org", "https://cdn.example.org/mail/")
		c.Assert(err, qt.IsNil)
		c.Assert(b.AssetsURL, qt.Equals, "https://cdn.example.org/mail")
		c.Assert(contrast(b.AccentDark, darkCard) >= minContrast, qt.IsTrue, qt.Commentf("%s -> %s", accent, b.AccentDark))
		c.Assert(contrast(b.AccentLabel, accent) >= minContrast, qt.IsTrue, qt.Commentf("%s label %s", accent, b.AccentLabel))
	}
	b, err = NewBrand("#ffcc00", "https://api.example.org", "")
	c.Assert(err, qt.IsNil)
	c.Assert(b.AccentLabel, qt.Equals, "#000000")

	for _, bad := range []string{"", "1d62e9", "#1d62e", "#1d62e9ff", "red", "#gggggg"} {
		_, err := NewBrand(bad, "https://api.example.org", "")
		c.Assert(err, qt.ErrorMatches, "invalid mail accent colour.*")
	}
	_, err = NewBrand(DefaultAccent, "", "")
	c.Assert(err, qt.ErrorMatches, "the server URL is required")
	c.Assert(Load(Brand{}), qt.ErrorMatches, "invalid brand colour.*")
}

func TestToMap(t *testing.T) {
	c := qt.New(t)
	m, err := toMap(CodeData{Code: "x"})
	c.Assert(err, qt.IsNil)
	c.Assert(m["Code"], qt.Equals, "x")
	m, err = toMap(&struct{ Code string }{"y"})
	c.Assert(err, qt.IsNil)
	c.Assert(m["Code"], qt.Equals, "y")
	_, err = toMap(struct{ Brand string }{"z"})
	c.Assert(err, qt.ErrorMatches, `.*"Brand" field, it is reserved`)
	_, err = toMap("not a struct")
	c.Assert(err, qt.ErrorMatches, "mail data must be a struct or a map.*")
}

func TestMissingFieldFails(t *testing.T) {
	c := qt.New(t)
	_, err := VerifyAccountNotification.Localized("en").ExecTemplate(struct{ Code, Link string }{"x", "y"})
	c.Assert(err, qt.ErrorMatches, `.*the data has no field "Expiry".*`)
}

func TestOrgLogoURL(t *testing.T) {
	c := qt.New(t)
	const server = "https://api.example.org"
	for raw, want := range map[string]string{
		server + "/storage/0f3a.png":                       server + "/storage/0f3a.png",
		server + "/storage/0f3a.jpeg":                      server + "/storage/0f3a.jpeg",
		server + "/storage/0f3a.JPG":                       server + "/storage/0f3a.JPG",
		server + "//storage/0f3a.webp":                     server + "/storage/0f3a.webp",
		"/storage/0f3a.gif":                                server + "/storage/0f3a.gif",
		" " + server + "/storage/0f3a.png ":                server + "/storage/0f3a.png",
		server + "/storage/0f3a.svg":                       "",
		server + "/storage/0f3a.json":                      "",
		server + "/storage/":                               "",
		server + "/storage/a/../0f3a.png":                  "",
		server + "/storage/0f3a.png?x=.png":                "",
		"https://tracker.example.com/pixel.png":            "",
		"http://api.example.org.evil.com/storage/0f3a.png": "",
		"data:image/png;base64,AAAA":                       "",
		"https://gateway.example.com/ipfs/bafkrei":         "",
		"": "",
	} {
		c.Assert(orgLogoURL(raw, server), qt.Equals, want, qt.Commentf("%q", raw))
	}
}

func TestNewMembersImportData(t *testing.T) {
	c := qt.New(t)
	issues := make([]string, 3000)
	for i := range issues {
		issues[i] = strings.Repeat("é", 200)
	}
	for _, tc := range []struct {
		r     MembersImportResult
		state string
		shown int
		more  int
	}{
		{MembersImportResult{Total: 10, Added: 10}, ImportDone, 0, 0},
		{MembersImportResult{Total: 10, Added: 7, Errors: issues[:3]}, ImportIssues, 3, 0},
		{MembersImportResult{Total: 3250, Added: 250, Errors: issues}, ImportIssues, 10, 2990},
		{MembersImportResult{Total: 3000, Added: 0, Errors: issues}, ImportFailed, 10, 2990},
		{MembersImportResult{}, ImportEmpty, 0, 0},
	} {
		d := NewMembersImportData(tc.r)
		c.Assert(d.State, qt.Equals, tc.state)
		c.Assert(d.Issues, qt.HasLen, tc.shown)
		c.Assert(d.MoreIssues, qt.Equals, tc.more)
		c.Assert(d.ErrorCount, qt.Equals, len(tc.r.Errors))
		for _, issue := range d.Issues {
			c.Assert(utf8.RuneCountInString(issue), qt.Equals, ImportIssueMaxRunes)
		}
	}
	c.Assert(NewMembersImportData(MembersImportResult{UserName: "  "}).UserName, qt.Equals, "")
}

func TestNewSupportData(t *testing.T) {
	c := qt.New(t)
	short := NewSupportData("Bug", "Title", "line 1\nline 2", "a@b.c", "Org")
	c.Assert(short.Truncated, qt.IsFalse)
	c.Assert(short.Description, qt.Equals, "line 1\nline 2")
	long := NewSupportData("Bug", "Title", strings.Repeat("à", 5000), "a@b.c", "Org")
	c.Assert(long.Truncated, qt.IsTrue)
	c.Assert(utf8.RuneCountInString(long.Description), qt.Equals, SupportDescriptionMaxRunes)

	// Type and Title are capped to protect email size.
	longType := NewSupportData(strings.Repeat("x", 200), "Title", "desc", "a@b.c", "Org")
	c.Assert(utf8.RuneCountInString(longType.Type), qt.Equals, SupportTypeMaxRunes)
	longTitle := NewSupportData("Bug", strings.Repeat("à", 500), "desc", "a@b.c", "Org")
	c.Assert(utf8.RuneCountInString(longTitle.Title), qt.Equals, SupportTitleMaxRunes)
}
