package api

import (
	"bytes"
	"context"
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
	"strings"
	"sync/atomic"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/errors"
	"github.com/vocdoni/saas-backend/internal"
	"go.mongodb.org/mongo-driver/v2/bson"
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

// testImageServer serves data as a PNG image at /image.png and anything else as a 404, counting
// the requests it gets.
func testImageServer(t *testing.T, data []byte) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	hits := &atomic.Int32{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.URL.Path != "/image.png" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(data)
	}))
	t.Cleanup(srv.Close)
	return srv, hits
}

// storedObject returns the bytes the object storage serves at a local URL.
func storedObject(t *testing.T, localURL string) []byte {
	t.Helper()
	data, code := testRequest(t, http.MethodGet, "", nil, "storage", path.Base(localURL))
	qt.Assert(t, code, qt.Equals, http.StatusOK)
	return data
}

// isLocalURL reports whether u is served by the test server's object storage.
func isLocalURL(u string) bool {
	return strings.Contains(u, "/storage/")
}

// assertParentMediaHashes publishes req and checks that the parent election's document commits
// exactly the given meta.mediaHashes (the stream never among them) next to its question
// elections, and that the on-chain metadata hash covers the whole document.
func assertParentMediaHashes(t *testing.T, token, pid string, want map[string]any) {
	t.Helper()
	c := qt.New(t)
	job := enqueueAndPollJob(t, http.MethodPost, token, nil, "processes", pid, "publish")
	c.Assert(job.Status, qt.Equals, db.JobStatusCompleted, qt.Commentf("job error: %s", job.Errors))
	got := requestAndParse[apicommon.VotingProcessResponse](t, http.MethodGet, token, nil, "processes", pid)
	elections := make([]internal.HexBytes, 0, len(got.Questions))
	for _, q := range got.Questions {
		elections = append(elections, q.UpstreamID)
	}
	doc := storedObject(t, got.MetadataURL)

	var metadata dvoteapi.ElectionMetadata
	c.Assert(json.Unmarshal(doc, &metadata), qt.IsNil)
	c.Assert(metadata.Media.Header, qt.Equals, got.Header)
	c.Assert(metadata.Media.StreamURI, qt.Equals, got.StreamURI)
	c.Assert(metadata.Meta, qt.DeepEquals, map[string]any{
		"mediaHashes":       want,
		"questionElections": questionElectionsOf(elections),
	})

	docSum := sha256.Sum256(doc)
	c.Assert([]byte(got.MetadataHash), qt.DeepEquals, docSum[:])
	election, err := testNewVocdoniClient(t).Election(got.UpstreamID.Bytes())
	c.Assert(err, qt.IsNil)
	c.Assert([]byte(election.MetadataHash), qt.DeepEquals, docSum[:])
}

// createProcess creates req as a draft and returns its id.
func createProcess(t *testing.T, token string, req *apicommon.CreateVotingProcessRequest) string {
	t.Helper()
	return requestAndParse[apicommon.CreateVotingProcessResponse](
		t, http.MethodPost, token, req, processesCreateEndpoint,
	).ProcessID
}

// TestProcessMetadataMediaHashes checks that images are imported into the object storage when a
// process is saved, that the published documents commit the SHA-256 of their content (the
// header in the parent's, choice images in their question's), and that the video stream is
// never imported nor hashed, only its URL committed.
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
		pid := createProcess(t, token, mediaProcessRequest(orgAddress, members, headerURL, streamURI))
		assertParentMediaHashes(t, token, pid, map[string]any{headerURL: hex.EncodeToString(sum[:])})
	})

	t.Run("external header is imported", func(t *testing.T) {
		c := qt.New(t)
		header := testPNG(t)
		srv, _ := testImageServer(t, header)
		pid := createProcess(t, token, mediaProcessRequest(orgAddress, members, srv.URL+"/image.png", streamURI))
		draft := requestAndParse[apicommon.VotingProcessResponse](t, http.MethodGet, token, nil, "processes", pid)
		c.Assert(isLocalURL(draft.Header), qt.IsTrue, qt.Commentf("header %s", draft.Header))
		c.Assert(storedObject(t, draft.Header), qt.DeepEquals, header)
		c.Assert(draft.StreamURI, qt.Equals, streamURI)
		sum := sha256.Sum256(header)
		assertParentMediaHashes(t, token, pid, map[string]any{draft.Header: hex.EncodeToString(sum[:])})
	})

	t.Run("choice images", func(t *testing.T) {
		c := qt.New(t)
		local := testPNG(t)
		localURL := testUploadImage(t, token, local)
		external := testPNG(t)
		srv, hits := testImageServer(t, external)
		externalURL := srv.URL + "/image.png"
		localSum, externalSum := sha256.Sum256(local), sha256.Sum256(external)

		req := mediaProcessRequest(orgAddress, members, "", streamURI)
		req.Questions[0].Metadata = map[string]any{"choices": []any{
			map[string]any{"value": 0, "image": localURL},
			// the same url twice is fetched and hashed once
			map[string]any{"value": 1, "image": map[string]any{"default": externalURL, "thumbnail": externalURL}},
		}}
		pid := createProcess(t, token, req)
		c.Assert(hits.Load(), qt.Equals, int32(1))
		draft := requestAndParse[apicommon.VotingProcessResponse](t, http.MethodGet, token, nil, "processes", pid)
		entries, ok := draft.Questions[0].Metadata["choices"].([]any)
		c.Assert(ok, qt.IsTrue)
		c.Assert(entries, qt.HasLen, 2)
		image, ok := entries[1].(map[string]any)["image"].(map[string]any)
		c.Assert(ok, qt.IsTrue)
		importedURL, ok := image["default"].(string)
		c.Assert(ok, qt.IsTrue)
		c.Assert(isLocalURL(importedURL), qt.IsTrue)
		c.Assert(image["thumbnail"], qt.Equals, importedURL)
		c.Assert(storedObject(t, importedURL), qt.DeepEquals, external)

		job := enqueueAndPollJob(t, http.MethodPost, token, nil, "processes", pid, "publish")
		c.Assert(job.Status, qt.Equals, db.JobStatusCompleted, qt.Commentf("job error: %s", job.Errors))
		got := requestAndParse[apicommon.VotingProcessResponse](t, http.MethodGet, token, nil, "processes", pid)

		var parentDoc dvoteapi.ElectionMetadata
		c.Assert(json.Unmarshal(storedObject(t, got.MetadataURL), &parentDoc), qt.IsNil)
		parentMeta, ok := parentDoc.Meta.(map[string]any)
		c.Assert(ok, qt.IsTrue)
		_, hasHashes := parentMeta["mediaHashes"]
		c.Assert(hasHashes, qt.IsFalse) // no header; choice images belong to the question document

		doc := storedObject(t, got.Questions[0].MetadataURL)
		var metadata dvoteapi.ElectionMetadata
		c.Assert(json.Unmarshal(doc, &metadata), qt.IsNil)
		c.Assert(metadata.Meta, qt.DeepEquals, map[string]any{"mediaHashes": map[string]any{
			localURL:    hex.EncodeToString(localSum[:]),
			importedURL: hex.EncodeToString(externalSum[:]),
		}})
		c.Assert(metadata.Questions[0].Choices[0].Meta, qt.DeepEquals, map[string]any{"image": localURL})
		docSum := sha256.Sum256(doc)
		c.Assert([]byte(got.Questions[0].MetadataHash), qt.DeepEquals, docSum[:])
	})

	t.Run("unimportable images refuse the save", func(t *testing.T) {
		srv, _ := testImageServer(t, testPNG(t))
		html := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte("<html><body>not an image</body></html>"))
		}))
		t.Cleanup(html.Close)
		for _, tc := range []struct {
			name, url, field, reason string
		}{
			{"unreachable", srv.URL + "/missing.png", "header", "not reachable"},
			{"not an image", html.URL, "header", "not an image"},
			{"private address", "http://10.255.255.1/image.png", "header", "not allowed"},
		} {
			t.Run(tc.name, func(t *testing.T) {
				before, err := testDB.CountVotingProcesses(orgAddress, db.AllProcesses)
				qt.Assert(t, err, qt.IsNil)
				apiErr := requestAndParseWithAssertCode[errors.Error](http.StatusUnprocessableEntity, t,
					http.MethodPost, token, mediaProcessRequest(orgAddress, members, tc.url, streamURI),
					processesCreateEndpoint)
				qt.Assert(t, apiErr.Code, qt.Equals, errors.ErrMediaUnavailable.Code)
				qt.Assert(t, apiErr.Error(), qt.Contains, tc.field+": "+tc.url)
				qt.Assert(t, apiErr.Error(), qt.Contains, tc.reason)
				after, err := testDB.CountVotingProcesses(orgAddress, db.AllProcesses)
				qt.Assert(t, err, qt.IsNil)
				qt.Assert(t, after, qt.Equals, before)
			})
		}

		t.Run("choice image", func(t *testing.T) {
			req := mediaProcessRequest(orgAddress, members, "", streamURI)
			req.Questions[0].Metadata = map[string]any{"choices": []any{
				map[string]any{"value": 1, "image": srv.URL + "/missing.png"},
			}}
			apiErr := requestAndParseWithAssertCode[errors.Error](http.StatusUnprocessableEntity, t,
				http.MethodPost, token, req, processesCreateEndpoint)
			qt.Assert(t, apiErr.Code, qt.Equals, errors.ErrMediaUnavailable.Code)
			qt.Assert(t, apiErr.Error(), qt.Contains, "questions[0].choices[1].image: "+srv.URL+"/missing.png")
		})
	})

	t.Run("publish fetches nothing", func(t *testing.T) {
		c := qt.New(t)
		srv, hits := testImageServer(t, testPNG(t))
		pid := createProcess(t, token, mediaProcessRequest(orgAddress, members, "", streamURI))
		// a header that bypassed the import, as only a direct write could leave it
		oid, err := bson.ObjectIDFromHex(pid)
		c.Assert(err, qt.IsNil)
		_, err = testDB.DBClient.Database(testDBName).Collection("votingProcesses").UpdateOne(context.Background(),
			bson.M{"_id": oid}, bson.M{"$set": bson.M{"header": srv.URL + "/image.png"}})
		c.Assert(err, qt.IsNil)
		requestAndAssertError(errors.ErrMediaUnavailable, t, http.MethodPost, token, nil, "processes", pid, "publish")
		c.Assert(hits.Load(), qt.Equals, int32(0))
		vp := requestAndParse[apicommon.VotingProcessResponse](t, http.MethodGet, token, nil, "processes", pid)
		c.Assert(vp.Published, qt.IsFalse)
	})
}

// TestImageFetchPolicy checks the external image fetcher: production refuses loopback, private,
// link-local, shared and unspecified addresses at dial time, and only http(s) URLs are fetched.
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

	srv, _ := testImageServer(t, testPNG(t))
	_, err := fetchImage(newMediaHTTPClient(isPublicIP), srv.URL+"/image.png")
	c.Assert(err, qt.ErrorMatches, `.*not allowed.*`)

	// a redirect to a refused address is refused too
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://10.255.255.1/image.png", http.StatusFound)
	}))
	defer redirect.Close()
	_, err = fetchImage(newMediaHTTPClient(mediaIPAllowed), redirect.URL)
	c.Assert(err, qt.ErrorMatches, `.*not allowed.*`)

	_, err = fetchImage(newMediaHTTPClient(mediaIPAllowed), srv.URL+"/image.png")
	c.Assert(err, qt.IsNil)

	_, err = fetchImage(newMediaHTTPClient(mediaIPAllowed), "file:///etc/passwd")
	c.Assert(err, qt.ErrorMatches, `.*unsupported scheme.*`)
}
