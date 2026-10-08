package api

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/httptest"
	"path"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/errors"
	"github.com/vocdoni/saas-backend/internal"
	dvoteapi "go.vocdoni.io/dvote/api"
)

// testUploadImage uploads data through the object storage upload handler and returns the URL
// it answers with.
func testUploadImage(t *testing.T, token string, data []byte) string {
	t.Helper()
	c := qt.New(t)
	body := &bytes.Buffer{}
	form := multipart.NewWriter(body)
	part, err := form.CreateFormFile("file", "header.png")
	c.Assert(err, qt.IsNil)
	_, err = part.Write(data)
	c.Assert(err, qt.IsNil)
	c.Assert(form.Close(), qt.IsNil)

	req, err := http.NewRequest(http.MethodPost,
		fmt.Sprintf("http://%s:%d%s", testHost, testPort, objectStorageUploadTypedEndpoint), body)
	c.Assert(err, qt.IsNil)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", form.FormDataContentType())
	resp, err := http.DefaultClient.Do(req)
	c.Assert(err, qt.IsNil)
	defer func() { _ = resp.Body.Close() }()
	respBody, err := io.ReadAll(resp.Body)
	c.Assert(err, qt.IsNil)
	c.Assert(resp.StatusCode, qt.Equals, http.StatusOK, qt.Commentf("body: %s", respBody))

	var uploaded struct {
		URLs []string `json:"urls"`
	}
	c.Assert(json.Unmarshal(respBody, &uploaded), qt.IsNil)
	c.Assert(uploaded.URLs, qt.HasLen, 1)
	return uploaded.URLs[0]
}

// testPNG returns a small PNG whose pixel is random, so each call yields distinct bytes.
func testPNG(t *testing.T) []byte {
	t.Helper()
	rnd := internal.RandomBytes(3)
	img := image.NewRGBA(image.Rect(0, 0, 1, 1))
	img.Set(0, 0, color.RGBA{R: rnd[0], G: rnd[1], B: rnd[2], A: 255})
	buf := &bytes.Buffer{}
	qt.Assert(t, png.Encode(buf, img), qt.IsNil)
	return buf.Bytes()
}

// mediaProcessRequest returns a one-question process draft with the given header and stream,
// whose census is the given members.
func mediaProcessRequest(orgAddress common.Address, members []apicommon.OrgMember, header, streamURI string,
) *apicommon.CreateVotingProcessRequest {
	req := minimalVotingProcessRequest(orgAddress)
	req.StartDate = ""
	req.Header = header
	req.StreamURI = streamURI
	req.Census = apicommon.CensusSpec{
		TwoFaFields: db.OrgMemberTwoFaFields{db.OrgMemberTwoFaFieldEmail},
		MemberIDs:   memberIDs(members),
	}
	return req
}

// testImageServer serves data as a PNG image at /image.png and anything else as a 404.
func testImageServer(t *testing.T, data []byte) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/image.png" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(data)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// assertParentMediaHashes publishes req and checks that the parent election's document commits
// exactly the given meta.mediaHashes (the stream never among them) next to its question
// elections, and that the on-chain metadata hash covers the whole document.
func assertParentMediaHashes(t *testing.T, token string, req *apicommon.CreateVotingProcessRequest,
	want map[string]any,
) {
	t.Helper()
	c := qt.New(t)
	pid, elections := publishProcessRequest(t, token, req)
	got := requestAndParse[apicommon.VotingProcessResponse](t, http.MethodGet, token, nil, "processes", pid)
	doc, code := testRequest(t, http.MethodGet, "", nil, "storage", path.Base(got.MetadataURL))
	c.Assert(code, qt.Equals, http.StatusOK)

	var metadata dvoteapi.ElectionMetadata
	c.Assert(json.Unmarshal(doc, &metadata), qt.IsNil)
	c.Assert(metadata.Media.Header, qt.Equals, req.Header)
	c.Assert(metadata.Media.StreamURI, qt.Equals, req.StreamURI)
	c.Assert(metadata.Meta, qt.DeepEquals, map[string]any{
		"mediaHashes":       want,
		"questionElections": questionElectionsOf(elections),
	})

	docSum := sha256.Sum256(doc)
	c.Assert([]byte(got.MetadataHash), qt.DeepEquals, docSum[:])
	election, err := testNewVocdoniClient(t).Election(got.UpstreamID.Bytes())
	c.Assert(err, qt.IsNil)
	c.Assert([]byte(election.MetadataHash), qt.DeepEquals, docSum[:])

	// the question documents carry no media and so no hashes
	for _, q := range got.Questions {
		qdoc, code := testRequest(t, http.MethodGet, "", nil, "storage", path.Base(q.MetadataURL))
		c.Assert(code, qt.Equals, http.StatusOK)
		var qmeta dvoteapi.ElectionMetadata
		c.Assert(json.Unmarshal(qdoc, &qmeta), qt.IsNil)
		c.Assert(qmeta.Meta, qt.IsNil)
	}
}

// TestProcessMetadataMediaHashes checks that the parent election's document commits the SHA-256
// of every image by content, whether this backend stores it or it is external, and never hashes
// the video stream, of which only the URL is committed.
func TestProcessMetadataMediaHashes(t *testing.T) {
	token := testCreateUser(t, "mediapassword123")
	orgAddress := testCreateProvisionedOrganization(t, token)
	setOrganizationSubscription(t, orgAddress, mockEssentialPlan.ID)
	members := postOrgMembers(t, token, orgAddress, newOrgMembers(1)...)
	const streamURI = "https://www.youtube.com/watch?v=dQw4w9WgXcQ"

	t.Run("stored header", func(t *testing.T) {
		header := testPNG(t)
		headerURL := testUploadImage(t, token, header)
		sum := sha256.Sum256(header)
		assertParentMediaHashes(t, token, mediaProcessRequest(orgAddress, members, headerURL, streamURI),
			map[string]any{headerURL: hex.EncodeToString(sum[:])})
	})

	t.Run("external header", func(t *testing.T) {
		header := testPNG(t)
		headerURL := testImageServer(t, header).URL + "/image.png"
		sum := sha256.Sum256(header)
		assertParentMediaHashes(t, token, mediaProcessRequest(orgAddress, members, headerURL, streamURI),
			map[string]any{headerURL: hex.EncodeToString(sum[:])})
	})

	t.Run("choice images", func(t *testing.T) {
		c := qt.New(t)
		local := testPNG(t)
		localURL := testUploadImage(t, token, local)
		external := testPNG(t)
		externalURL := testImageServer(t, external).URL + "/image.png"
		localSum, externalSum := sha256.Sum256(local), sha256.Sum256(external)

		req := mediaProcessRequest(orgAddress, members, "", streamURI)
		req.Questions[0].Metadata = map[string]any{"choices": []any{
			map[string]any{"value": 0, "image": localURL},
			// the same url twice is hashed once
			map[string]any{"value": 1, "image": map[string]any{"default": externalURL, "thumbnail": localURL}},
		}}
		pid, _ := publishProcessRequest(t, token, req)
		got := requestAndParse[apicommon.VotingProcessResponse](t, http.MethodGet, token, nil, "processes", pid)

		parent, code := testRequest(t, http.MethodGet, "", nil, "storage", path.Base(got.MetadataURL))
		c.Assert(code, qt.Equals, http.StatusOK)
		var parentDoc dvoteapi.ElectionMetadata
		c.Assert(json.Unmarshal(parent, &parentDoc), qt.IsNil)
		parentMeta, ok := parentDoc.Meta.(map[string]any)
		c.Assert(ok, qt.IsTrue)
		_, hasHashes := parentMeta["mediaHashes"]
		c.Assert(hasHashes, qt.IsFalse) // no header; choice images belong to the question document

		doc, code := testRequest(t, http.MethodGet, "", nil, "storage", path.Base(got.Questions[0].MetadataURL))
		c.Assert(code, qt.Equals, http.StatusOK)
		var metadata dvoteapi.ElectionMetadata
		c.Assert(json.Unmarshal(doc, &metadata), qt.IsNil)
		c.Assert(metadata.Meta, qt.DeepEquals, map[string]any{"mediaHashes": map[string]any{
			localURL:    hex.EncodeToString(localSum[:]),
			externalURL: hex.EncodeToString(externalSum[:]),
		}})
		c.Assert(metadata.Questions[0].Choices[0].Meta, qt.DeepEquals, map[string]any{"image": localURL})
		docSum := sha256.Sum256(doc)
		c.Assert([]byte(got.Questions[0].MetadataHash), qt.DeepEquals, docSum[:])
	})

	t.Run("unreachable choice image refuses the publish", func(t *testing.T) {
		req := mediaProcessRequest(orgAddress, members, "", streamURI)
		req.Questions[0].Metadata = map[string]any{"choices": []any{
			map[string]any{"value": 0, "image": testImageServer(t, testPNG(t)).URL + "/missing.png"},
		}}
		pid := requestAndParse[apicommon.CreateVotingProcessResponse](
			t, http.MethodPost, token, req, processesCreateEndpoint,
		).ProcessID
		requestAndAssertError(errors.ErrMediaUnavailable, t, http.MethodPost, token, nil, "processes", pid, "publish")
	})

	t.Run("unreachable header refuses the publish", func(t *testing.T) {
		headerURL := testImageServer(t, testPNG(t)).URL + "/missing.png"
		req := mediaProcessRequest(orgAddress, members, headerURL, streamURI)
		pid := requestAndParse[apicommon.CreateVotingProcessResponse](
			t, http.MethodPost, token, req, processesCreateEndpoint,
		).ProcessID
		requestAndAssertError(errors.ErrMediaUnavailable, t, http.MethodPost, token, nil, "processes", pid, "publish")
		vp := requestAndParse[apicommon.VotingProcessResponse](t, http.MethodGet, token, nil, "processes", pid)
		qt.Assert(t, vp.Published, qt.IsFalse)
	})
}

// TestImageFetchPolicy checks the external image fetcher: production refuses loopback, private,
// link-local and unspecified addresses at dial time, and a fetch only accepts an image body.
func TestImageFetchPolicy(t *testing.T) {
	c := qt.New(t)
	for _, tc := range []struct {
		ip     string
		public bool
	}{
		{"8.8.8.8", true},
		{"2606:4700:4700::1111", true},
		{"127.0.0.1", false},
		{"::1", false},
		{"10.1.2.3", false},
		{"172.16.0.1", false},
		{"192.168.1.1", false},
		{"169.254.169.254", false},
		{"fe80::1", false},
		{"fd00::1", false},
		{"100.64.0.1", false},
		{"0.0.0.0", false},
		{"::", false},
	} {
		c.Assert(isPublicIP(net.ParseIP(tc.ip)), qt.Equals, tc.public, qt.Commentf("ip %s", tc.ip))
	}

	srv := testImageServer(t, testPNG(t))
	_, err := fetchImage(newMediaHTTPClient(isPublicIP), srv.URL+"/image.png")
	c.Assert(err, qt.ErrorMatches, `.*not allowed.*`)

	_, err = fetchImage(newMediaHTTPClient(mediaIPAllowed), srv.URL+"/image.png")
	c.Assert(err, qt.IsNil)

	html := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte("<html></html>"))
	}))
	defer html.Close()
	_, err = fetchImage(newMediaHTTPClient(mediaIPAllowed), html.URL)
	c.Assert(err, qt.ErrorMatches, `.*not an image.*`)

	_, err = fetchImage(newMediaHTTPClient(mediaIPAllowed), "file:///etc/passwd")
	c.Assert(err, qt.ErrorMatches, `.*unsupported scheme.*`)
}
