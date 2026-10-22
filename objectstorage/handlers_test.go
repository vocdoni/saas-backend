package objectstorage

import (
	"bytes"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"

	qt "github.com/frankban/quicktest"
)

func TestObjectIDFromName(t *testing.T) {
	c := qt.New(t)
	cases := []struct {
		name   string
		in     string
		wantID string
		wantOK bool
	}{
		{"png", "abc123.png", "abc123", true},
		{"jpg", "Img9.jpg", "Img9", true},
		{"jpeg", "Img9.jpeg", "Img9", true},
		{"json", "deadbeef.json", "deadbeef", true},
		{"double extension rejected", "abc.json.bak", "", false},
		{"path traversal rejected", "abc.jpg/../../etc/passwd", "", false},
		{"no extension rejected", "abc123", "", false},
		{"unknown extension rejected", "abc.gif", "", false},
		{"non-alphanumeric id rejected", "ab_c.png", "", false},
		{"empty rejected", "", "", false},
	}
	for _, tc := range cases {
		c.Run(tc.name, func(c *qt.C) {
			id, ok := objectIDfromName(tc.in)
			c.Assert(ok, qt.Equals, tc.wantOK)
			c.Assert(id, qt.Equals, tc.wantID)
		})
	}
}

func TestParseMultipartFormBodyTooLarge(t *testing.T) {
	c := qt.New(t)
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	part, err := mw.CreateFormFile("file", "big.png")
	c.Assert(err, qt.IsNil)
	_, err = part.Write(bytes.Repeat([]byte{0}, MaxUploadBodySize+1))
	c.Assert(err, qt.IsNil)
	c.Assert(mw.Close(), qt.IsNil)

	req := httptest.NewRequest(http.MethodPost, "/storage", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	rec := httptest.NewRecorder()
	c.Assert(parseMultipartForm(rec, req), qt.IsFalse)
	c.Assert(rec.Code, qt.Equals, http.StatusRequestEntityTooLarge)
}

func TestProcessFileRejectsUnsupportedTypeAsBadRequest(t *testing.T) {
	c := qt.New(t)
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	part, err := mw.CreateFormFile("file", "notes.txt")
	c.Assert(err, qt.IsNil)
	_, err = part.Write([]byte("plain text, not an image"))
	c.Assert(err, qt.IsNil)
	c.Assert(mw.Close(), qt.IsNil)

	req := httptest.NewRequest(http.MethodPost, "/storage", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	c.Assert(req.ParseMultipartForm(32*1024), qt.IsNil)
	fileHeader := req.MultipartForm.File["file"][0]

	// the client must carry the real supported types, otherwise every type would be rejected
	osc, err := New(&Config{})
	c.Assert(err, qt.IsNil)
	c.Assert(osc.supportedTypes[FileTypePNG], qt.IsTrue)

	rec := httptest.NewRecorder()
	_, ok := osc.processFile(rec, fileHeader, "user@example.com")
	c.Assert(ok, qt.IsFalse)
	c.Assert(rec.Code, qt.Equals, http.StatusBadRequest)
}

func TestPutRejectsEmptyAndOversizedObjects(t *testing.T) {
	c := qt.New(t)
	osc := &Client{}
	_, err := osc.Put(bytes.NewReader(nil), 0, "user@example.com")
	c.Assert(err, qt.ErrorIs, ErrorObjectEmpty)
	// the declared size is checked before anything is allocated or read
	_, err = osc.Put(bytes.NewReader(nil), MaxObjectSize+1, "user@example.com")
	c.Assert(err, qt.ErrorIs, ErrorObjectTooLarge)
}
