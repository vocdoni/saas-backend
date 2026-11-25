package mailtemplates

import (
	"bytes"
	"fmt"
	htmltemplate "html/template"
	"reflect"
	"regexp"
	"strings"
	"sync/atomic"
	texttemplate "text/template"
	"time"

	// rendering must not depend on the host's tzdata for Europe/Madrid
	_ "time/tzdata"

	root "github.com/vocdoni/saas-backend"
	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/notifications"
)

const (
	// assetsDir is the embedded directory with the layout, components, emails and strings.
	assetsDir = "assets/mail"
	// dateZone is the fixed zone dates are shown in.
	dateZone = "Europe/Madrid"
	// MaxSubjectRunes caps a subject after interpolation (RFC 5322 recommended line length).
	MaxSubjectRunes = 78
	// MaxHTMLBytes is the size budget of a rendered HTML body; Gmail clips at about 102 KB.
	MaxHTMLBytes = 60 * 1024
)

// MailTemplate is one kind of email: assets/mail/<File>.html (HTML blocks), assets/mail/<File>.txt
// (subject, preheader and plain-text blocks) and the <File> section of every i18n/<lang>.yaml.
type MailTemplate struct {
	File      string // base name, e.g. "verification_account"
	WebAppURI string // web app path the email links to, if any
}

// LocalizedTemplate is a MailTemplate in one language.
type LocalizedTemplate struct {
	file string
	lang string
}

// Localized returns the template in lang, or in apicommon.DefaultLang when lang is not a supported
// language. Callers pass normalized (lowercase) codes, see apicommon.NormalizeLang.
func (mt MailTemplate) Localized(lang string) LocalizedTemplate {
	if !apicommon.IsValidLang(lang) {
		lang = apicommon.DefaultLang
	}
	return LocalizedTemplate{file: mt.File, lang: lang}
}

// compiled holds the parsed templates of one email.
type compiled struct {
	html *htmltemplate.Template
	text *texttemplate.Template
}

// state is everything Load prepares; it is replaced atomically, so rendering never races Load.
type state struct {
	brand   Brand
	zone    *time.Location
	locales map[string]*locale
	emails  map[string]*compiled
}

var loaded atomic.Pointer[state]

// stringRefRgx finds the string keys templates pass to the .L methods.
var stringRefRgx = regexp.MustCompile(`\.L\.(HTML|Text|Subject)(n?)\s+"([A-Za-z0-9_.]+)"`)

// Load parses the layout, the components, every email and every locale, checks them, renders
// every fixture in every language, and only then makes them available. Any missing or malformed
// piece is an error, so the service fails at start-up rather than when a user triggers a mail.
func Load(brand Brand) error {
	if err := brand.validate(); err != nil {
		return err
	}
	zone, err := time.LoadLocation(dateZone)
	if err != nil {
		return fmt.Errorf("could not load the %s time zone: %w", dateZone, err)
	}
	st := &state{brand: brand, zone: zone, locales: map[string]*locale{}, emails: map[string]*compiled{}}
	for _, lang := range apicommon.SupportedLangs {
		data, err := root.Assets.ReadFile(assetsDir + "/i18n/" + lang + ".yaml")
		if err != nil {
			return fmt.Errorf("could not read the %s strings: %w", lang, err)
		}
		if st.locales[lang], err = parseLocale(lang, data); err != nil {
			return err
		}
	}
	en := st.locales[apicommon.DefaultLang]
	for _, loc := range st.locales {
		if err := loc.checkAgainst(en); err != nil {
			return err
		}
	}
	for _, mt := range All {
		c, err := compile(mt.File, en)
		if err != nil {
			return err
		}
		st.emails[mt.File] = c
	}
	for _, fx := range Fixtures() {
		for _, lang := range apicommon.SupportedLangs {
			if err := st.check(LocalizedTemplate{file: fx.Template.File, lang: lang}, fx.Data); err != nil {
				return fmt.Errorf("fixture %s in %s: %w", fx.Name, lang, err)
			}
		}
	}
	loaded.Store(st)
	return nil
}

// check renders one fixture completely, as ExecTemplate would.
func (st *state) check(lt LocalizedTemplate, data any) error {
	r, err := st.newRender(lt, data)
	if err != nil {
		return err
	}
	n, err := r.plain()
	if err != nil {
		return err
	}
	_, err = r.html(n.Subject)
	return err
}

// compile parses one email and checks that every string key its templates use exists in English
// with the shape the method expects.
func compile(file string, en *locale) (*compiled, error) {
	paths := []string{assetsDir + "/layout.html", assetsDir + "/components.html", assetsDir + "/" + file + ".html"}
	sources := ""
	for _, p := range append(paths, assetsDir+"/"+file+".txt") {
		src, err := root.Assets.ReadFile(p)
		if err != nil {
			return nil, fmt.Errorf("could not read %s: %w", p, err)
		}
		sources += string(src)
	}
	for _, ref := range stringRefRgx.FindAllStringSubmatch(sources, -1) {
		key := ref[3]
		if !strings.Contains(key, ".") {
			key = file + "." + key
		}
		e, ok := en.entries[key]
		if !ok {
			return nil, fmt.Errorf("%s uses the string %q, missing in i18n/en.yaml", file, key)
		}
		if want := map[string]string{"": "string", "n": "plural"}[ref[2]]; e.shape() != want {
			return nil, fmt.Errorf("%s uses %q as a %s, i18n/en.yaml has a %s", file, key, want, e.shape())
		}
	}
	h, err := htmltemplate.New("layout").Funcs(htmlFuncs).ParseFS(root.Assets, paths...)
	if err != nil {
		return nil, fmt.Errorf("could not parse %s: %w", file, err)
	}
	t, err := texttemplate.New("text").ParseFS(root.Assets, assetsDir+"/"+file+".txt")
	if err != nil {
		return nil, fmt.Errorf("could not parse %s.txt: %w", file, err)
	}
	for _, name := range []string{"subject", "preheader", "plain"} {
		if t.Lookup(name) == nil {
			return nil, fmt.Errorf("%s.txt does not define %q", file, name)
		}
	}
	for _, name := range []string{"header", "content", "footer"} {
		if h.Lookup(name) == nil {
			return nil, fmt.Errorf("%s.html does not define %q", file, name)
		}
	}
	return &compiled{html: h, text: t}, nil
}

// htmlFuncs emit the Outlook conditional comments, which html/template would strip from the
// template text.
var htmlFuncs = htmltemplate.FuncMap{
	"msoIf":     func() htmltemplate.HTML { return "<!--[if mso]>" },
	"msoEnd":    func() htmltemplate.HTML { return "<![endif]-->" },
	"notMsoIf":  func() htmltemplate.HTML { return "<!--[if !mso]><!-->" },
	"notMsoEnd": func() htmltemplate.HTML { return "<!--<![endif]-->" },
	"msoHead": func() htmltemplate.HTML {
		return "<!--[if mso]><noscript><xml><o:OfficeDocumentSettings><o:PixelsPerInch>96</o:PixelsPerInch>" +
			"</o:OfficeDocumentSettings></xml></noscript><![endif]-->"
	},
	"preheaderPad": func() htmltemplate.HTML { return htmltemplate.HTML(strings.Repeat("&zwnj;&nbsp;", 90)) },
	"dict":         dict,
}

// dict builds the argument map of a component: dict "Label" x "URL" y.
func dict(pairs ...any) (map[string]any, error) {
	if len(pairs)%2 != 0 {
		return nil, fmt.Errorf("dict needs key/value pairs")
	}
	m := make(map[string]any, len(pairs)/2)
	for i := 0; i < len(pairs); i += 2 {
		key, ok := pairs[i].(string)
		if !ok {
			return nil, fmt.Errorf("dict keys are strings")
		}
		m[key] = pairs[i+1]
	}
	return m, nil
}

// ExecTemplate renders the subject, the HTML body and the plain-text body with data, a struct (or
// a map) whose fields the templates and strings read. The brand is added as .Brand.
func (lt LocalizedTemplate) ExecTemplate(data any) (*notifications.Notification, error) {
	r, err := newRender(lt, data)
	if err != nil {
		return nil, err
	}
	n, err := r.plain()
	if err != nil {
		return nil, err
	}
	if n.Body, err = r.html(n.Subject); err != nil {
		return nil, err
	}
	return n, nil
}

// ExecPlain renders only the subject and the plain-text body, as SMS needs.
func (lt LocalizedTemplate) ExecPlain(data any) (*notifications.Notification, error) {
	r, err := newRender(lt, data)
	if err != nil {
		return nil, err
	}
	return r.plain()
}

// rendering is one email being rendered in one language.
type rendering struct {
	file   string
	email  *compiled
	l      *localizer
	fields map[string]any
}

func newRender(lt LocalizedTemplate, data any) (*rendering, error) {
	st := loaded.Load()
	if st == nil {
		return nil, fmt.Errorf("mail templates are not loaded")
	}
	return st.newRender(lt, data)
}

func (st *state) newRender(lt LocalizedTemplate, data any) (*rendering, error) {
	email, ok := st.emails[lt.file]
	if !ok {
		return nil, fmt.Errorf("unknown mail template %q", lt.file)
	}
	loc, ok := st.locales[lt.lang]
	if !ok {
		return nil, fmt.Errorf("unknown mail language %q", lt.lang)
	}
	fields, err := toMap(data)
	if err != nil {
		return nil, err
	}
	l := &localizer{
		Lang:   lt.lang,
		file:   lt.file,
		f:      formatter{loc: loc, en: st.locales[apicommon.DefaultLang], zone: st.zone},
		brand:  st.brand,
		fields: fields,
	}
	fields["Brand"] = st.brand
	fields["L"] = l
	return &rendering{file: lt.file, email: email, l: l, fields: fields}, nil
}

// text renders one block of <file>.txt, tidied.
func (r *rendering) text(name string) (string, error) {
	var buf bytes.Buffer
	if err := r.email.text.ExecuteTemplate(&buf, name, r.fields); err != nil {
		return "", fmt.Errorf("could not render %s %s: %w", r.file, name, err)
	}
	return tidy(buf.String()), nil
}

// plain renders the subject and the plain-text body.
func (r *rendering) plain() (*notifications.Notification, error) {
	subject, err := r.text("subject")
	if err != nil {
		return nil, err
	}
	body, err := r.text("plain")
	if err != nil {
		return nil, err
	}
	return &notifications.Notification{Subject: subject, PlainBody: body}, nil
}

// html renders the HTML body, titled with the subject.
func (r *rendering) html(subject string) (string, error) {
	preheader, err := r.text("preheader")
	if err != nil {
		return "", err
	}
	r.l.Title, r.l.Preheader = subject, preheader
	var buf bytes.Buffer
	if err := r.email.html.ExecuteTemplate(&buf, "layout", r.fields); err != nil {
		return "", fmt.Errorf("could not render %s html: %w", r.file, err)
	}
	return buf.String(), nil
}

// toMap turns the call-site data (a struct, a pointer to one, or a map) into the template context.
func toMap(data any) (map[string]any, error) {
	out := map[string]any{}
	if data == nil {
		return out, nil
	}
	if m, ok := data.(map[string]any); ok {
		for k, v := range m {
			out[k] = v
		}
	} else {
		v := reflect.ValueOf(data)
		for v.Kind() == reflect.Pointer {
			v = v.Elem()
		}
		if v.Kind() != reflect.Struct {
			return nil, fmt.Errorf("mail data must be a struct or a map, got %T", data)
		}
		for i := range v.NumField() {
			if field := v.Type().Field(i); field.IsExported() {
				out[field.Name] = v.Field(i).Interface()
			}
		}
	}
	for _, reserved := range []string{"Brand", "L"} {
		if _, ok := out[reserved]; ok {
			return nil, fmt.Errorf("mail data must not have a %q field, it is reserved", reserved)
		}
	}
	return out, nil
}

// tidy trims trailing spaces, collapses runs of blank lines and trims the text.
func tidy(s string) string {
	lines := strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n")
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimRight(line, " \t")
		if line == "" && len(out) > 0 && out[len(out)-1] == "" {
			continue
		}
		out = append(out, line)
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

// localizer is .L in the templates: it renders the strings of one email in one language against
// the data, and formats values.
type localizer struct {
	// Lang is the language code, for the lang attributes.
	Lang string
	// Title is the rendered subject, for <title> and aria-label.
	Title string
	// Preheader is the rendered preheader.
	Preheader string
	file      string
	f         formatter
	brand     Brand
	fields    map[string]any
}

// HTML renders a string as HTML: text and values escaped, *emphasis* as <strong>.
func (l *localizer) HTML(key string) (htmltemplate.HTML, error) {
	s, err := l.render(key, nil, asHTML)
	return htmltemplate.HTML(s), err //nolint:gosec // render escapes all text and values
}

// HTMLn renders the plural form of a string for count n as HTML.
func (l *localizer) HTMLn(key string, n any) (htmltemplate.HTML, error) {
	s, err := l.render(key, n, asHTML)
	return htmltemplate.HTML(s), err //nolint:gosec // render escapes all text and values
}

// Text renders a string as plain text.
func (l *localizer) Text(key string) (string, error) {
	return l.render(key, nil, asText)
}

// Textn renders the plural form of a string for count n as plain text.
func (l *localizer) Textn(key string, n any) (string, error) {
	return l.render(key, n, asText)
}

// Subject renders a string as plain text in at most MaxSubjectRunes, truncating the values.
func (l *localizer) Subject(key string) (string, error) {
	return l.fitted(key, nil)
}

// Subjectn renders the plural form of a subject for count n.
func (l *localizer) Subjectn(key string, n any) (string, error) {
	return l.fitted(key, n)
}

// V formats a value for the reader (numbers, durations, dates).
func (l *localizer) V(v any) (string, error) {
	return l.f.value(v)
}

// Lines splits text into lines, so templates can keep its line breaks with <br>.
func (*localizer) Lines(s string) []string {
	return strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n")
}

// Initial returns the first letter of name, upper-cased, for the monogram tile.
func (*localizer) Initial(name string) string {
	for _, r := range strings.TrimSpace(name) {
		return strings.ToUpper(string(r))
	}
	return ""
}

// OrgLogo returns the data's OrganizationLogo when it may be shown, else "".
func (l *localizer) OrgLogo() string {
	raw, ok := l.fields["OrganizationLogo"].(string)
	if !ok {
		return ""
	}
	return orgLogoURL(raw, l.brand.ServerURL)
}

func (l *localizer) message(key string, n any) (*message, error) {
	if !strings.Contains(key, ".") {
		key = l.file + "." + key
	}
	e, err := l.f.lookup(key)
	if err != nil {
		return nil, err
	}
	if n == nil {
		if e.msg == nil {
			return nil, fmt.Errorf("string %q is not a plain string", key)
		}
		return e.msg, nil
	}
	count, ok := asInt(n)
	if !ok || e.one == nil {
		return nil, fmt.Errorf("string %q is not a plural or %v is not a count", key, n)
	}
	return pick(e, count), nil
}

func (l *localizer) render(key string, n any, mode renderMode) (string, error) {
	m, err := l.message(key, n)
	if err != nil {
		return "", err
	}
	return m.render(mode, l.field)
}

func (l *localizer) fitted(key string, n any) (string, error) {
	m, err := l.message(key, n)
	if err != nil {
		return "", err
	}
	return m.renderFitted(MaxSubjectRunes, l.field)
}

// field returns the formatted value of a data field a string interpolates.
func (l *localizer) field(name string) (string, error) {
	v, ok := l.fields[name]
	if !ok {
		return "", fmt.Errorf("the data has no field %q", name)
	}
	return l.f.value(v)
}
