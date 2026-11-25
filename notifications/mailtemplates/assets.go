package mailtemplates

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"net/http"
	"sync"
	"time"

	root "github.com/vocdoni/saas-backend"
)

// AssetsRoute is the public route that serves the Vocdoni logos the emails link to.
const AssetsRoute = "/mail/assets/{file}"

// asset is one embedded image with its strong ETag.
type asset struct {
	data []byte
	etag string
}

// assets indexes assets/mail/images once; only these exact names are ever served.
var assets = sync.OnceValues(func() (map[string]asset, error) {
	entries, err := fs.ReadDir(root.Assets, assetsDir+"/images")
	if err != nil {
		return nil, err
	}
	out := make(map[string]asset, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		data, err := root.Assets.ReadFile(assetsDir + "/images/" + e.Name())
		if err != nil {
			return nil, err
		}
		sum := sha256.Sum256(data)
		out[e.Name()] = asset{data: data, etag: `"` + hex.EncodeToString(sum[:16]) + `"`}
	}
	return out, nil
})

// ServeAsset writes the embedded mail image called name, or 404 for any other name (no paths, no
// listing). Images are cached for a day and revalidated by ETag; a changed artwork ships under a
// new file name.
func ServeAsset(w http.ResponseWriter, r *http.Request, name string) {
	index, err := assets()
	if err != nil {
		http.Error(w, "mail assets unavailable", http.StatusInternalServerError)
		return
	}
	a, ok := index[name]
	if !ok {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Cache-Control", "public, max-age=86400")
	w.Header().Set("ETag", a.etag)
	http.ServeContent(w, r, name, time.Time{}, bytes.NewReader(a.data))
}
