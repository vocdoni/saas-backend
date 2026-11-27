// Command mailpreview renders every email in every language and fixture state to a directory, so
// they can be opened in a browser without sending anything:
//
//	go run ./cmd/mailpreview -out ~/.cache/saas-backend/preview [-accent '#166534']
package main

import (
	"bytes"
	"flag"
	"fmt"
	"html"
	"image"
	"image/color"
	"image/png"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	root "github.com/vocdoni/saas-backend"
	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/notifications/mailtemplates"
)

func main() {
	out := flag.String("out", "", "output directory (required)")
	accent := flag.String("accent", mailtemplates.DefaultAccent, "accent colour, #rrggbb")
	flag.Parse()
	if *out == "" {
		flag.Usage()
		os.Exit(2)
	}
	if err := run(*out, *accent); err != nil {
		log.Fatal(err)
	}
}

func run(out, accent string) error {
	// the logos are copied next to the pages, so the preview works offline
	brand, err := mailtemplates.NewBrand(accent, mailtemplates.DefaultBrand().ServerURL, "images")
	if err != nil {
		return err
	}
	if err := mailtemplates.Load(brand); err != nil {
		return err
	}
	if err := copyImages(filepath.Join(out, "images")); err != nil {
		return err
	}
	if err := writeOrgLogo(filepath.Join(out, "org-logo.png"), brand.Accent); err != nil {
		return err
	}
	var index strings.Builder
	index.WriteString(`<!DOCTYPE html><html><head><meta charset="utf-8"><title>Mail preview</title>
<style>body{font-family:system-ui,sans-serif;margin:32px}td{padding:4px 12px 4px 0;font-size:14px}</style>
</head><body><h1>Mail preview</h1><table><tr><th>Email</th><th>Language</th><th>Subject</th><th>HTML</th></tr>`)
	over := 0
	for _, fx := range mailtemplates.Fixtures() {
		for _, lang := range apicommon.SupportedLangs {
			n, err := fx.Template.Localized(lang).ExecTemplate(fx.Data)
			if err != nil {
				return fmt.Errorf("%s in %s: %w", fx.Name, lang, err)
			}
			name := fx.Name + "_" + lang
			// show the sample organization logo, which lives in a server the preview does not run
			body := strings.ReplaceAll(n.Body, mailtemplates.FixtureOrgLogo, "org-logo.png")
			if err := os.WriteFile(filepath.Join(out, name+".html"), []byte(body), 0o600); err != nil {
				return err
			}
			text := "Subject: " + n.Subject + "\n\n" + n.PlainBody + "\n"
			if err := os.WriteFile(filepath.Join(out, name+".txt"), []byte(text), 0o600); err != nil {
				return err
			}
			size := len(n.Body)
			if size > mailtemplates.MaxHTMLBytes || utf8.RuneCountInString(n.Subject) > mailtemplates.MaxSubjectRunes {
				over++
				log.Printf("over budget: %s (%d bytes, subject %d characters)", name, size, utf8.RuneCountInString(n.Subject))
			}
			fmt.Fprintf(&index, `<tr><td>%s</td><td>%s</td><td>%s</td>`, html.EscapeString(fx.Name), lang,
				html.EscapeString(n.Subject))
			fmt.Fprintf(&index, `<td><a href="%s.html">%.1f KB</a> · <a href="%s.txt">text</a></td></tr>`,
				name, float64(size)/1024, name)
		}
	}
	index.WriteString("</table></body></html>\n")
	if err := os.WriteFile(filepath.Join(out, "index.html"), []byte(index.String()), 0o600); err != nil {
		return err
	}
	log.Printf("wrote %d emails to %s", len(mailtemplates.Fixtures())*len(apicommon.SupportedLangs), out)
	if over > 0 {
		return fmt.Errorf("%d emails over the size or subject budget", over)
	}
	return nil
}

// copyImages writes the embedded logos to dir.
func copyImages(dir string) error {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	entries, err := fs.ReadDir(root.Assets, "assets/mail/images")
	if err != nil {
		return err
	}
	for _, e := range entries {
		data, err := root.Assets.ReadFile("assets/mail/images/" + e.Name())
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(dir, e.Name()), data, 0o600); err != nil {
			return err
		}
	}
	return nil
}

// writeOrgLogo writes a plain square in the accent colour as the sample organization logo.
func writeOrgLogo(path, accent string) error {
	var r, g, b uint8
	if _, err := fmt.Sscanf(accent, "#%02x%02x%02x", &r, &g, &b); err != nil {
		return err
	}
	img := image.NewRGBA(image.Rect(0, 0, 96, 96))
	for x := range 96 {
		for y := range 96 {
			img.Set(x, y, color.RGBA{R: r, G: g, B: b, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return err
	}
	return os.WriteFile(path, buf.Bytes(), 0o600)
}
