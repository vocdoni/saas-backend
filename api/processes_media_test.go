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
	"net/http"
	"path"
	"testing"

	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/internal"
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

// TestProcessMetadataMediaHashes publishes a process whose header is stored by this backend and
// whose stream is external: the published metadata document commits the SHA-256 of the stored
// header bytes in meta.mediaHashes and leaves the external stream out, and the on-chain metadata
// hash still covers the whole document.
func TestProcessMetadataMediaHashes(t *testing.T) {
	c := qt.New(t)
	token := testCreateUser(t, "mediapassword123")
	orgAddress := testCreateProvisionedOrganization(t, token)
	setOrganizationSubscription(t, orgAddress, mockEssentialPlan.ID)
	members := postOrgMembers(t, token, orgAddress, newOrgMembers(1)...)

	header := testPNG(t)
	headerURL := testUploadImage(t, token, header)
	headerSum := sha256.Sum256(header)
	served, code := testRequest(t, http.MethodGet, "", nil, "storage", path.Base(headerURL))
	c.Assert(code, qt.Equals, http.StatusOK)
	c.Assert(served, qt.DeepEquals, header)
	const streamURI = "https://www.youtube.com/watch?v=dQw4w9WgXcQ"

	req := minimalVotingProcessRequest(orgAddress)
	req.StartDate = ""
	req.Header = headerURL
	req.StreamURI = streamURI
	req.Census = apicommon.CensusSpec{
		TwoFaFields: db.OrgMemberTwoFaFields{db.OrgMemberTwoFaFieldEmail},
		MemberIDs:   memberIDs(members),
	}
	pid, _ := publishProcessRequest(t, token, req)

	got := requestAndParse[apicommon.VotingProcessResponse](t, http.MethodGet, token, nil, "processes", pid)
	c.Assert(got.Questions, qt.HasLen, 1)
	q := got.Questions[0]
	doc, code := testRequest(t, http.MethodGet, "", nil, "storage", path.Base(q.MetadataURL))
	c.Assert(code, qt.Equals, http.StatusOK)

	var metadata struct {
		Media struct {
			Header    string `json:"header"`
			StreamURI string `json:"streamUri"`
		} `json:"media"`
		Meta struct {
			MediaHashes map[string]string `json:"mediaHashes"`
		} `json:"meta"`
	}
	c.Assert(json.Unmarshal(doc, &metadata), qt.IsNil)
	c.Assert(metadata.Media.Header, qt.Equals, headerURL)
	c.Assert(metadata.Media.StreamURI, qt.Equals, streamURI)
	c.Assert(metadata.Meta.MediaHashes, qt.DeepEquals, map[string]string{
		headerURL: hex.EncodeToString(headerSum[:]),
	})

	docSum := sha256.Sum256(doc)
	c.Assert([]byte(q.MetadataHash), qt.DeepEquals, docSum[:])
	election, err := testNewVocdoniClient(t).Election(q.UpstreamID.Bytes())
	c.Assert(err, qt.IsNil)
	c.Assert([]byte(election.MetadataHash), qt.DeepEquals, docSum[:])
}
