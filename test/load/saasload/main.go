// Command saasload prepares fixtures and vegeta target files for the saas-backend load tests in
// test/load. It talks to a disposable saas-backend copy over its API and to its MongoDB directly
// (to seed a plan, verify the admin and list member ids). Never point it at production.
//
//	saasload setup    -api URL -mongo URL -db NAME -members N -questions 1,5 -out fixture.json
//	saasload auth     -fixture fixture.json -process 0 -from 0 -n 1000 -out DIR
//	saasload sign     -fixture fixture.json -process 0 -dir DIR
//	saasload votes    -fixture fixture.json -process 0 -dir DIR [-batch]
//	saasload jobs     -fixture fixture.json -results vote.results.jsonl -out jobs.targets
//	saasload reads    -fixture fixture.json -out reads.targets
//	saasload jobstats -mongo URL -db NAME -since RFC3339 [-type relay_vote]
//	saasload verify   -fixture fixture.json -mongo URL -db NAME -out verify.targets
//
// Every target carries the voter index as ?v=N, so a vegeta result (which records the URL but not
// the request body) can be matched back to its voter by the next stage.
package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"math/big"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/internal"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.vocdoni.io/dvote/crypto/ethereum"
	"go.vocdoni.io/proto/build/go/models"
	"google.golang.org/protobuf/proto"
)

// Fixture is everything the later stages need about the seeded organization and processes.
type Fixture struct {
	API       string           `json:"api"`
	Token     string           `json:"token"`
	Org       string           `json:"org"`
	ChainID   string           `json:"chainId"`
	Members   int              `json:"members"`
	Processes []FixtureProcess `json:"processes"`
}

// FixtureProcess is one published process: its saas id and one on-chain election per question.
type FixtureProcess struct {
	ID          string   `json:"id"`
	UpstreamIDs []string `json:"upstreamIds"`
}

// Voter is one census member with the key it votes with.
type Voter struct {
	Index   int    `json:"i"`
	Member  string `json:"member"`
	Key     string `json:"key"`
	Address string `json:"address"`
}

// target is a vegeta JSON target.
type target struct {
	Method string              `json:"method"`
	URL    string              `json:"url"`
	Header map[string][]string `json:"header,omitempty"`
	Body   string              `json:"body,omitempty"`
}

// signature is one ballot of a sign-batch response.
type signature struct {
	UpstreamID string `json:"upstreamId"`
	Signature  string `json:"signature"`
	Weight     string `json:"weight"`
}

// result is the subset of a vegeta JSON result (vegeta encode -to=json) the stages read.
type result struct {
	Code int    `json:"code"`
	URL  string `json:"url"`
	Body string `json:"body"`
}

func main() {
	log.SetFlags(log.Ltime)
	if len(os.Args) < 2 {
		log.Fatal("usage: saasload setup|auth|sign|votes|jobs|reads|jobstats [flags]")
	}
	cmd, args := os.Args[1], os.Args[2:]
	var err error
	switch cmd {
	case "setup":
		err = setup(args)
	case "auth":
		err = authTargets(args)
	case "sign":
		err = signTargets(args)
	case "votes":
		err = voteTargets(args)
	case "jobs":
		err = jobTargets(args)
	case "reads":
		err = readTargets(args)
	case "jobstats":
		err = jobStats(args)
	case "verify":
		err = verifyTargets(args)
	default:
		err = fmt.Errorf("unknown command %q", cmd)
	}
	if err != nil {
		log.Fatal(err)
	}
}

// client is a minimal JSON client for the saas API.
type client struct {
	base  string
	token string
	http  *http.Client
}

func (c *client) do(method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, c.base+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode >= 300 {
		return fmt.Errorf("%s %s: %d %s", method, path, resp.StatusCode, data)
	}
	if out != nil {
		return json.Unmarshal(data, out)
	}
	return nil
}

// waitJob polls GET /jobs/{id} until the job leaves pending.
func (c *client) waitJob(jobID string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		var job db.Job
		if err := c.do(http.MethodGet, "/jobs/"+jobID, nil, &job); err != nil {
			return err
		}
		switch job.Status {
		case db.JobStatusCompleted:
			return nil
		case db.JobStatusFailed:
			return fmt.Errorf("job %s failed: %s %v", jobID, job.Error, job.Errors)
		}
		time.Sleep(2 * time.Second)
	}
	return fmt.Errorf("job %s still pending after %s", jobID, timeout)
}

func setup(args []string) error {
	fs := flag.NewFlagSet("setup", flag.ExitOnError)
	api := fs.String("api", "http://127.0.0.1:8080", "saas-backend base URL")
	mongoURL := fs.String("mongo", "mongodb://127.0.0.1:27017", "MongoDB URL of the target")
	dbName := fs.String("db", "saasload", "MongoDB database of the target")
	members := fs.Int("members", 20000, "census size")
	questions := fs.String("questions", "1,5", "comma-separated question count of each process to create")
	out := fs.String("out", "fixture.json", "fixture output file")
	_ = fs.Parse(args)

	store, err := db.New(*mongoURL, *dbName)
	if err != nil {
		return fmt.Errorf("mongo: %w", err)
	}
	// a default plan large enough that no quota gets in the way of the test
	const unlimited = 10_000_000
	if err := store.SetPlan(&db.Plan{
		ID: "load-test", Name: "Load test", Default: true, Public: true,
		Organization: db.PlanLimits{
			Users: 100, SubOrgs: 10, MaxProcesses: unlimited, MaxCensus: unlimited, MaxVotes: unlimited,
			MaxDuration: 365, MaxDrafts: unlimited, CustomPlan: true,
		},
		VotingTypes: db.VotingTypes{Single: true, Multiple: true, Approval: true, Cumulative: true, Ranked: true},
		Features:    db.Features{LiveResults: true, Overwrite: true, TwoFaEmail: unlimited, TwoFaSms: unlimited},
	}); err != nil {
		return fmt.Errorf("plan: %w", err)
	}

	c := &client{base: strings.TrimRight(*api, "/"), http: &http.Client{Timeout: 5 * time.Minute}}
	email := fmt.Sprintf("load-%d@load.test", time.Now().Unix())
	const password = "load-test-password"
	if err := c.do(http.MethodPost, "/users", &apicommon.UserInfo{
		Email: email, Password: password, FirstName: "Load", LastName: "Test",
	}, nil); err != nil {
		return err
	}
	user, err := store.UserByEmail(email)
	if err != nil {
		return err
	}
	if err := store.VerifyUserAccount(user); err != nil {
		return err
	}
	var login apicommon.LoginResponse
	if err := c.do(http.MethodPost, "/auth/login", &apicommon.UserInfo{Email: email, Password: password}, &login); err != nil {
		return err
	}
	c.token = login.Token

	var org apicommon.OrganizationInfo
	if err := c.do(http.MethodPost, "/organizations", &apicommon.CreateOrganizationRequest{
		OrganizationInfo: apicommon.OrganizationInfo{Type: "company", Website: "https://load.test"},
		ProvisionAccount: true,
	}, &org); err != nil {
		return err
	}
	orgPath := "/organizations/" + org.Address.Hex()
	log.Printf("organization %s", org.Address.Hex())

	// members carry only a member number: an auth-only census needs no OTP, and neither a
	// phone nor a password, which would cost an argon2 hash per member at import
	const chunk = 5000
	for from := 0; from < *members; from += chunk {
		batch := make([]apicommon.OrgMember, 0, chunk)
		for i := from; i < min(from+chunk, *members); i++ {
			batch = append(batch, apicommon.OrgMember{MemberNumber: memberNumber(i), Name: "Voter", Surname: strconv.Itoa(i)})
		}
		if err := c.do(http.MethodPost, orgPath+"/members", &apicommon.AddMembersRequest{Members: batch}, nil); err != nil {
			return err
		}
	}
	memberIDs := make([]string, 0, *members)
	for page := int64(1); ; page++ {
		_, ms, err := store.OrgMembers(org.Address, db.OrgMembersQuery{Page: page, Limit: 1000})
		if err != nil {
			return err
		}
		for _, m := range ms {
			memberIDs = append(memberIDs, m.ID.Hex())
		}
		if len(ms) < 1000 {
			break
		}
	}
	log.Printf("%d members", len(memberIDs))

	fx := &Fixture{API: c.base, Token: c.token, Org: org.Address.Hex(), Members: len(memberIDs)}
	for _, qs := range strings.Split(*questions, ",") {
		nq, err := strconv.Atoi(qs)
		if err != nil {
			return err
		}
		p, chainID, err := createProcess(c, org.Address, memberIDs, nq)
		if err != nil {
			return err
		}
		fx.ChainID = chainID
		fx.Processes = append(fx.Processes, *p)
		log.Printf("process %s with %d questions published", p.ID, nq)
	}
	return writeJSON(*out, fx)
}

func memberNumber(i int) string { return fmt.Sprintf("M%07d", i) }

func createProcess(c *client, org common.Address, memberIDs []string, questions int) (*FixtureProcess, string, error) {
	req := &apicommon.CreateVotingProcessRequest{
		OrgAddress: org.Bytes(),
		Census: apicommon.CensusSpec{
			AuthFields: db.OrgMemberAuthFields{db.OrgMemberAuthFieldsMemberNumber},
			MemberIDs:  memberIDs,
		},
		Title:   db.MultiLangString{"default": fmt.Sprintf("Load test, %d questions", questions)},
		EndDate: time.Now().Add(7 * 24 * time.Hour).UTC().Format(time.RFC3339),
	}
	for q := range questions {
		req.Questions = append(req.Questions, apicommon.VotingProcessQuestionRequest{
			Title: db.MultiLangString{"default": fmt.Sprintf("Q%d", q+1)},
			Choices: []db.Choice{
				{Title: db.MultiLangString{"default": "No"}, Value: 0},
				{Title: db.MultiLangString{"default": "Yes"}, Value: 1},
			},
			Type:      db.VotingTypeSingleChoice,
			TypeSetup: db.QuestionTypeSetup{MinChoices: 1, MaxChoices: 1},
		})
	}
	var created apicommon.CreateVotingProcessResponse
	if err := c.do(http.MethodPost, "/processes", req, &created); err != nil {
		return nil, "", err
	}
	var job apicommon.EnqueuedResponse
	if err := c.do(http.MethodPost, "/processes/"+created.ProcessID+"/publish", nil, &job); err != nil {
		return nil, "", err
	}
	if err := c.waitJob(job.JobID, 10*time.Minute); err != nil {
		return nil, "", err
	}
	var p apicommon.VotingProcessResponse
	if err := c.do(http.MethodGet, "/processes/"+created.ProcessID, nil, &p); err != nil {
		return nil, "", err
	}
	fp := &FixtureProcess{ID: created.ProcessID}
	for _, q := range p.Questions {
		if len(q.UpstreamID) == 0 {
			return nil, "", fmt.Errorf("process %s question %s has no upstream id", p.ID, q.ID.Hex())
		}
		fp.UpstreamIDs = append(fp.UpstreamIDs, q.UpstreamID.String())
	}
	return fp, p.ChainID, nil
}

// authTargets generates voters [from, from+n) with fresh keys and one POST auth/0 target each.
func authTargets(args []string) error {
	fs := flag.NewFlagSet("auth", flag.ExitOnError)
	fxPath := fs.String("fixture", "fixture.json", "fixture file")
	pi := fs.Int("process", 0, "process index in the fixture")
	from := fs.Int("from", 0, "first voter index")
	n := fs.Int("n", 1000, "number of voters")
	dir := fs.String("out", ".", "output directory")
	_ = fs.Parse(args)
	fx, err := readFixture(*fxPath)
	if err != nil {
		return err
	}
	if *from+*n > fx.Members {
		return fmt.Errorf("voters %d..%d exceed the census of %d", *from, *from+*n, fx.Members)
	}
	p := fx.Processes[*pi]
	if err := os.MkdirAll(*dir, 0o755); err != nil {
		return err
	}
	voters := make([]Voter, 0, *n)
	var targets []target
	for i := *from; i < *from+*n; i++ {
		key := ethereum.NewSignKeys()
		if err := key.Generate(); err != nil {
			return err
		}
		_, priv := key.HexString()
		v := Voter{Index: i, Member: memberNumber(i), Key: priv, Address: hex.EncodeToString(key.Address().Bytes())}
		voters = append(voters, v)
		body, err := json.Marshal(map[string]string{"memberNumber": v.Member})
		if err != nil {
			return err
		}
		targets = append(targets, postTarget(fmt.Sprintf("%s/processes/%s/auth/0?v=%d", fx.API, p.ID, i), body, ""))
	}
	if err := writeLines(filepath.Join(*dir, "voters.jsonl"), voters); err != nil {
		return err
	}
	return writeLines(filepath.Join(*dir, "auth.targets"), targets)
}

// signTargets turns the auth results into one POST sign-batch target per authenticated voter,
// covering every question of the process.
func signTargets(args []string) error {
	fs := flag.NewFlagSet("sign", flag.ExitOnError)
	fxPath := fs.String("fixture", "fixture.json", "fixture file")
	pi := fs.Int("process", 0, "process index in the fixture")
	dir := fs.String("dir", ".", "directory with voters.jsonl and auth.results.jsonl")
	_ = fs.Parse(args)
	fx, err := readFixture(*fxPath)
	if err != nil {
		return err
	}
	p := fx.Processes[*pi]
	voters, err := readVoters(filepath.Join(*dir, "voters.jsonl"))
	if err != nil {
		return err
	}
	tokens := map[int]string{}
	if err := eachResult(filepath.Join(*dir, "auth.results.jsonl"), func(i int, body []byte) error {
		var r struct {
			AuthToken string `json:"authToken"`
		}
		if err := json.Unmarshal(body, &r); err != nil || r.AuthToken == "" {
			return nil
		}
		tokens[i] = r.AuthToken
		return nil
	}); err != nil {
		return err
	}
	if err := writeJSON(filepath.Join(*dir, "tokens.json"), tokens); err != nil {
		return err
	}
	var targets []target
	for _, v := range voters {
		tok, ok := tokens[v.Index]
		if !ok {
			continue
		}
		req := map[string]any{"authToken": tok}
		var ballots []map[string]string
		for _, up := range p.UpstreamIDs {
			ballots = append(ballots, map[string]string{"upstreamId": up, "address": v.Address})
		}
		req["ballots"] = ballots
		body, err := json.Marshal(req)
		if err != nil {
			return err
		}
		targets = append(targets, postTarget(fmt.Sprintf("%s/processes/%s/sign-batch?v=%d", fx.API, p.ID, v.Index), body, ""))
	}
	log.Printf("%d of %d voters authenticated", len(targets), len(voters))
	return writeLines(filepath.Join(*dir, "sign.targets"), targets)
}

// voteTargets builds and signs the vote txs of every voter with a CSP signature: one POST /vote
// target per ballot, or one POST /votes target per voter with -batch.
func voteTargets(args []string) error {
	fs := flag.NewFlagSet("votes", flag.ExitOnError)
	fxPath := fs.String("fixture", "fixture.json", "fixture file")
	pi := fs.Int("process", 0, "process index in the fixture")
	dir := fs.String("dir", ".", "directory with voters.jsonl and sign.results.jsonl")
	batch := fs.Bool("batch", false, "one POST /votes per voter instead of one POST /vote per ballot")
	_ = fs.Parse(args)
	fx, err := readFixture(*fxPath)
	if err != nil {
		return err
	}
	_ = fx.Processes[*pi]
	voters, err := readVoters(filepath.Join(*dir, "voters.jsonl"))
	if err != nil {
		return err
	}
	byIndex := map[int]Voter{}
	for _, v := range voters {
		byIndex[v.Index] = v
	}
	var targets []target
	signed := 0
	if err := eachResult(filepath.Join(*dir, "sign.results.jsonl"), func(i int, body []byte) error {
		var r struct {
			Signatures []signature `json:"signatures"`
		}
		if err := json.Unmarshal(body, &r); err != nil {
			return nil
		}
		v, ok := byIndex[i]
		if !ok {
			return nil
		}
		key := ethereum.NewSignKeys()
		if err := key.AddHexKey(v.Key); err != nil {
			return err
		}
		var payloads []apicommon.RelayVoteRequest
		for _, s := range r.Signatures {
			if s.Signature == "" {
				continue
			}
			stx, err := signedVote(key, fx.ChainID, s.UpstreamID, v.Address, s.Signature, s.Weight)
			if err != nil {
				return err
			}
			payloads = append(payloads, apicommon.RelayVoteRequest{TxPayload: stx})
		}
		if len(payloads) == 0 {
			return nil
		}
		signed++
		if *batch {
			b, err := json.Marshal(&apicommon.RelayVotesRequest{Votes: payloads})
			if err != nil {
				return err
			}
			targets = append(targets, postTarget(fmt.Sprintf("%s/votes?v=%d", fx.API, i), b, ""))
			return nil
		}
		for _, pl := range payloads {
			b, err := json.Marshal(&pl)
			if err != nil {
				return err
			}
			targets = append(targets, postTarget(fmt.Sprintf("%s/vote?v=%d", fx.API, i), b, ""))
		}
		return nil
	}); err != nil {
		return err
	}
	log.Printf("%d voters signed, %d vote targets", signed, len(targets))
	name := "vote.targets"
	if *batch {
		name = "votes.targets"
	}
	return writeLines(filepath.Join(*dir, name), targets)
}

// signedVote builds the CSP-proven vote envelope for one election and signs it as the voter.
func signedVote(key *ethereum.SignKeys, chainID, upstreamID, address, signature, weight string) (internal.HexBytes, error) {
	pid, err := hex.DecodeString(strings.TrimPrefix(upstreamID, "0x"))
	if err != nil {
		return nil, err
	}
	addr, err := hex.DecodeString(strings.TrimPrefix(address, "0x"))
	if err != nil {
		return nil, err
	}
	sig, err := hex.DecodeString(strings.TrimPrefix(signature, "0x"))
	if err != nil {
		return nil, err
	}
	w := big.NewInt(1).Bytes()
	if weight != "" {
		if w, err = hex.DecodeString(strings.TrimPrefix(weight, "0x")); err != nil {
			return nil, err
		}
	}
	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	pkg, err := json.Marshal(map[string][]int{"votes": {1}})
	if err != nil {
		return nil, err
	}
	tx, err := proto.Marshal(&models.Tx{Payload: &models.Tx_Vote{Vote: &models.VoteEnvelope{
		ProcessId: pid, Nonce: nonce, VotePackage: pkg,
		Proof: &models.Proof{Payload: &models.Proof_Ca{Ca: &models.ProofCA{
			Type: models.ProofCA_ECDSA_PIDSALTED, Signature: sig,
			Bundle: &models.CAbundle{ProcessId: pid, Address: addr, VoteWeight: w},
		}}},
	}}})
	if err != nil {
		return nil, err
	}
	txSig, err := key.SignVocdoniTx(tx, chainID)
	if err != nil {
		return nil, err
	}
	return proto.Marshal(&models.SignedTx{Tx: tx, Signature: txSig})
}

// jobTargets turns relay results (202 {"jobId"}) into GET /jobs/{id} targets, as the voter's
// client would poll them.
func jobTargets(args []string) error {
	fs := flag.NewFlagSet("jobs", flag.ExitOnError)
	fxPath := fs.String("fixture", "fixture.json", "fixture file")
	results := fs.String("results", "vote.results.jsonl", "vegeta JSON results of a relay run")
	out := fs.String("out", "jobs.targets", "output targets file")
	_ = fs.Parse(args)
	fx, err := readFixture(*fxPath)
	if err != nil {
		return err
	}
	var targets []target
	if err := eachResult(*results, func(i int, body []byte) error {
		var r apicommon.EnqueuedResponse
		if json.Unmarshal(body, &r) == nil && r.JobID != "" {
			targets = append(targets, target{Method: http.MethodGet, URL: fmt.Sprintf("%s/jobs/%s?v=%d", fx.API, r.JobID, i)})
		}
		return nil
	}); err != nil {
		return err
	}
	log.Printf("%d job targets", len(targets))
	return writeLines(*out, targets)
}

// readTargets writes the public read targets of every process: the process, its results and the
// question of each election, as a voter page and a results page load them.
func readTargets(args []string) error {
	fs := flag.NewFlagSet("reads", flag.ExitOnError)
	fxPath := fs.String("fixture", "fixture.json", "fixture file")
	out := fs.String("out", ".", "output directory")
	_ = fs.Parse(args)
	fx, err := readFixture(*fxPath)
	if err != nil {
		return err
	}
	for i, p := range fx.Processes {
		get := func(path string) target {
			return target{Method: http.MethodGet, URL: fx.API + path}
		}
		if err := writeLines(filepath.Join(*out, fmt.Sprintf("process%d.targets", i)),
			[]target{get("/processes/" + p.ID)}); err != nil {
			return err
		}
		if err := writeLines(filepath.Join(*out, fmt.Sprintf("results%d.targets", i)),
			[]target{get("/processes/" + p.ID + "/results")}); err != nil {
			return err
		}
	}
	return nil
}

// jobStats summarizes the jobs created since a point in time from MongoDB: outcome counts,
// creation-to-completion latency and the completion rate per 10 s.
func jobStats(args []string) error {
	fs := flag.NewFlagSet("jobstats", flag.ExitOnError)
	mongoURL := fs.String("mongo", "mongodb://127.0.0.1:27017", "MongoDB URL of the target")
	dbName := fs.String("db", "saasload", "MongoDB database of the target")
	since := fs.String("since", "", "RFC3339 lower bound of createdAt")
	typ := fs.String("type", "", "job type filter (e.g. relay_vote)")
	_ = fs.Parse(args)
	t0, err := time.Parse(time.RFC3339Nano, *since)
	if err != nil {
		return err
	}
	store, err := db.New(*mongoURL, *dbName)
	if err != nil {
		return err
	}
	filter := bson.M{"createdAt": bson.M{"$gte": t0}}
	if *typ != "" {
		filter["type"] = *typ
	}
	cur, err := store.DBClient.Database(*dbName).Collection("jobs").Find(context.Background(), filter)
	if err != nil {
		return err
	}
	var jobs []db.Job
	if err := cur.All(context.Background(), &jobs); err != nil {
		return err
	}
	counts := map[db.JobStatus]int{}
	errs := map[string]int{}
	var lat []float64
	buckets := map[int64]int{}
	var first, last time.Time
	for _, j := range jobs {
		counts[j.Status]++
		if j.Status == db.JobStatusFailed {
			msg := j.Error
			if len(msg) > 80 {
				msg = msg[:80]
			}
			errs[msg]++
		}
		if j.Status != db.JobStatusCompleted || j.CompletedAt.IsZero() {
			continue
		}
		lat = append(lat, j.CompletedAt.Sub(j.CreatedAt).Seconds())
		buckets[j.CompletedAt.Unix()/10]++
		if first.IsZero() || j.CompletedAt.Before(first) {
			first = j.CompletedAt
		}
		if j.CompletedAt.After(last) {
			last = j.CompletedAt
		}
	}
	slices.Sort(lat)
	pct := func(p float64) float64 {
		if len(lat) == 0 {
			return 0
		}
		return lat[min(len(lat)-1, int(p*float64(len(lat))))]
	}
	peak := 0
	for _, n := range buckets {
		peak = max(peak, n)
	}
	rate := 0.0
	if span := last.Sub(t0).Seconds(); span > 0 {
		rate = float64(len(lat)) / span
	}
	fmt.Printf("jobs=%d completed=%d failed=%d pending=%d completed_per_s=%.2f peak_10s=%.1f/s "+
		"latency_s p50=%.1f p95=%.1f p99=%.1f max=%.1f\n",
		len(jobs), counts[db.JobStatusCompleted], counts[db.JobStatusFailed], counts[db.JobStatusPending],
		rate, float64(peak)/10, pct(0.5), pct(0.95), pct(0.99), pct(1))
	for msg, n := range errs {
		fmt.Printf("  failed %d: %s\n", n, msg)
	}
	return nil
}

// verifyTargets writes one POST /votes/verify target per relayed vote on record, as a voter
// confirming that its ballot is on chain.
func verifyTargets(args []string) error {
	fs := flag.NewFlagSet("verify", flag.ExitOnError)
	fxPath := fs.String("fixture", "fixture.json", "fixture file")
	mongoURL := fs.String("mongo", "mongodb://127.0.0.1:27017", "MongoDB URL of the target")
	dbName := fs.String("db", "saasload", "MongoDB database of the target")
	out := fs.String("out", "verify.targets", "output targets file")
	_ = fs.Parse(args)
	fx, err := readFixture(*fxPath)
	if err != nil {
		return err
	}
	store, err := db.New(*mongoURL, *dbName)
	if err != nil {
		return err
	}
	cur, err := store.DBClient.Database(*dbName).Collection("jobs").Find(context.Background(),
		bson.M{"type": db.JobTypeRelayVote, "status": db.JobStatusCompleted})
	if err != nil {
		return err
	}
	var jobs []db.Job
	if err := cur.All(context.Background(), &jobs); err != nil {
		return err
	}
	var targets []target
	for i, j := range jobs {
		if j.Result == nil || len(j.Result.Nullifier) == 0 {
			continue
		}
		body, err := json.Marshal(&apicommon.VerifyVotesRequest{Nullifiers: []internal.HexBytes{j.Result.Nullifier}})
		if err != nil {
			return err
		}
		targets = append(targets, postTarget(fmt.Sprintf("%s/votes/verify?v=%d", fx.API, i), body, ""))
	}
	log.Printf("%d verify targets", len(targets))
	return writeLines(*out, targets)
}

func postTarget(u string, body []byte, token string) target {
	t := target{
		Method: http.MethodPost, URL: u, Header: map[string][]string{"Content-Type": {"application/json"}},
		Body: base64.StdEncoding.EncodeToString(body),
	}
	if token != "" {
		t.Header["Authorization"] = []string{"Bearer " + token}
	}
	return t
}

// eachResult calls fn with the voter index and body of every 2xx vegeta result in path.
func eachResult(path string, fn func(i int, body []byte) error) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	dec := json.NewDecoder(f)
	for {
		var r result
		if err := dec.Decode(&r); err == io.EOF {
			return nil
		} else if err != nil {
			return err
		}
		if r.Code < 200 || r.Code >= 300 {
			continue
		}
		u, err := url.Parse(r.URL)
		if err != nil {
			return err
		}
		i, err := strconv.Atoi(u.Query().Get("v"))
		if err != nil {
			continue
		}
		body, err := base64.StdEncoding.DecodeString(r.Body)
		if err != nil {
			return err
		}
		if err := fn(i, body); err != nil {
			return err
		}
	}
}

func readFixture(path string) (*Fixture, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var fx Fixture
	return &fx, json.Unmarshal(b, &fx)
}

func readVoters(path string) ([]Voter, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	var voters []Voter
	dec := json.NewDecoder(f)
	for {
		var v Voter
		if err := dec.Decode(&v); err == io.EOF {
			return voters, nil
		} else if err != nil {
			return nil, err
		}
		voters = append(voters, v)
	}
}

func writeJSON(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o644)
}

func writeLines[T any](path string, items []T) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	enc := json.NewEncoder(f)
	for _, it := range items {
		if err := enc.Encode(it); err != nil {
			_ = f.Close()
			return err
		}
	}
	return f.Close()
}
