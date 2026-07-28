// Package main implements a one-shot maintenance CLI that brings stored CSP
// login data in line with how the service now computes login hashes.
//
// It repairs two things that both make a member impossible to authenticate,
// because the login hash is a byte-exact match over the census auth fields:
//
//   - surrounding whitespace in name, surname, member number or national ID,
//     picked up from a spreadsheet or CSV import;
//   - hashes computed before the hash inputs were folded to lowercase, which is
//     what makes login case-insensitive.
//
// Member documents keep their original casing throughout — only the values that
// feed the hash are folded, and only the census participants' stored hashes are
// rewritten.
//
// It defaults to a dry run and only writes when --apply is passed:
//
//	repairlogins --mongoURL "$VOCDONI_MONGOURL" --mongoDB saas          # report only
//	repairlogins --mongoURL "$VOCDONI_MONGOURL" --mongoDB saas --apply  # repair
//	repairlogins ... --org 0xabc… --apply                               # one organization
//
// IMPORTANT: run this *after* deploying the service, never before. Deploying
// first and repairing second leaves a short window in which members whose hash
// changes cannot log in; repairing first would instead leave any census created
// in between holding unfolded hashes, with no second pass to catch them.
//
// It is idempotent: a second run finds nothing to trim and every hash already
// equal to its recomputed value. It connects to MongoDB directly rather than
// through db.New, so it runs no migrations and honours no VOCDONI_MONGO_RESET_DB.
package main

import (
	"context"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common"
	flag "github.com/spf13/pflag"
	"github.com/spf13/viper"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"go.mongodb.org/mongo-driver/mongo/readpref"
	"go.mongodb.org/mongo-driver/x/mongo/driver/connstring"
	"go.vocdoni.io/dvote/log"

	"github.com/vocdoni/saas-backend/migrations"
)

// runTimeout bounds the whole repair. Phase A scans orgMembers with a regex
// prefilter that no index can serve, and phase B walks every census participant,
// so the cost is proportional to the size of the member base.
const runTimeout = 60 * time.Minute

func main() {
	log.Init(log.LogLevelDebug, "stdout", nil)

	flag.StringP("mongoURL", "m", "", "MongoDB URL")
	flag.StringP("mongoDB", "d", "", "MongoDB database name")
	flag.Bool("apply", false, "write the changes; without it the run only reports what it would do")
	flag.String("org", "", "restrict the run to a single organization address (hex)")
	flag.Parse()

	viper.SetEnvPrefix("VOCDONI")
	if err := viper.BindPFlags(flag.CommandLine); err != nil {
		log.Fatalf("could not bind flags: %v", err)
	}
	viper.AutomaticEnv()

	mongoURL := viper.GetString("mongoURL")
	if mongoURL == "" {
		log.Fatal("mongoURL is required")
	}
	mongoDB := viper.GetString("mongoDB")
	if mongoDB == "" {
		log.Fatal("mongoDB is required")
	}
	apply := viper.GetBool("apply")

	opts := migrations.RepairOptions{Apply: apply}
	if rawOrg := viper.GetString("org"); rawOrg != "" {
		if !common.IsHexAddress(rawOrg) {
			log.Fatalf("invalid org address: %s", rawOrg)
		}
		org := common.HexToAddress(rawOrg)
		opts.OrgAddress = &org
	}

	ctx, cancel := context.WithTimeout(context.Background(), runTimeout)
	defer cancel()

	client, err := connect(ctx, mongoURL)
	if err != nil {
		log.Fatalf("could not connect to mongodb: %v", err)
	}
	defer func() {
		if err := client.Disconnect(context.Background()); err != nil {
			log.Warnw("error disconnecting from mongodb", "error", err)
		}
	}()

	if !apply {
		log.Infow("dry run: no changes will be written, pass --apply to write them")
	}
	report, err := migrations.RepairLoginHashes(ctx, client.Database(mongoDB), opts)
	if err != nil {
		log.Fatalf("could not repair login hashes: %v", err)
	}
	printReport(report, opts)
}

// connect opens a MongoDB connection, appending authSource=admin when the URL
// omits it, matching what db.New does so the same VOCDONI_MONGOURL works here.
func connect(ctx context.Context, url string) (*mongo.Client, error) {
	cs, err := connstring.ParseAndValidate(url)
	if err != nil {
		return nil, err
	}
	if !cs.AuthSourceSet {
		var sb strings.Builder
		sb.WriteString(url)
		switch {
		case strings.Contains(url, "?"):
			sb.WriteString("&")
		case strings.HasSuffix(url, "/"):
			sb.WriteString("?")
		default:
			sb.WriteString("/?")
		}
		sb.WriteString("authSource=admin")
		url = sb.String()
	}
	log.Infow("connecting to mongodb", "host", redactedHosts(cs))

	client, err := mongo.Connect(ctx, options.Client().ApplyURI(url))
	if err != nil {
		return nil, err
	}
	if err := client.Ping(ctx, readpref.Primary()); err != nil {
		return nil, err
	}
	return client, nil
}

// redactedHosts returns just the hosts of a parsed connection string, for
// logging. The connection URL itself carries the username and password, so it
// must never be logged: this command is run by hand against production, and its
// output lands in terminal scrollback, shell logs and CI job output.
func redactedHosts(cs *connstring.ConnString) string {
	return strings.Join(cs.Hosts, ",")
}

// printReport summarizes the run. Skipped participants are identified by member
// and census ID only: field values are member data and are never printed.
func printReport(report migrations.RepairReport, opts migrations.RepairOptions) {
	apply := opts.Apply
	verb := "were repaired"
	if !apply {
		verb = "would be repaired"
	}

	log.Infow("login hash repair summary",
		"membersScanned", report.MembersScanned,
		"membersTrimmed", report.MembersTrimmed,
		"censusesScanned", report.CensusesScanned,
		"participantsScanned", report.ParticipantsScanned,
		"participantsRehashed", report.ParticipantsRehashed,
		"participantsSkipped", report.ParticipantsSkipped,
		"orphanParticipants", report.OrphanParticipants,
		"censusesAffected", report.CensusesAffected,
		"applied", apply)

	if report.MembersTrimmed == 0 && report.ParticipantsRehashed == 0 && report.ParticipantsSkipped == 0 {
		log.Info("nothing to do: member fields are clean and every login hash is already up to date")
		return
	}
	log.Infow("member documents "+verb, "count", report.MembersTrimmed)
	log.Infow("participant login hashes "+verb,
		"count", report.ParticipantsRehashed, "censuses", report.CensusesAffected)

	if report.ParticipantsSkipped > 0 {
		log.Warnw("some participants were skipped: their recomputed login hash collides with "+
			"another participant of the same census, so they need manual review",
			"count", report.ParticipantsSkipped)
		for _, s := range report.SkippedMembers {
			log.Warnw("skipped participant", "memberID", s.MemberID, "censusID", s.CensusID)
		}
	}
	if report.OrphanParticipants > 0 {
		log.Warnw("some participants reference a member document that no longer exists; their "+
			"hashes cannot be recomputed and were left untouched. Those voters cannot "+
			"authenticate today either, because the CSP loads the member after matching the hash",
			"count", report.OrphanParticipants)
	}
	if !apply {
		log.Info("dry run complete: re-run with --apply to write these changes")
	}
}
