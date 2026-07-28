// Package main implements a one-shot maintenance CLI that strips leading and
// trailing whitespace from the member fields feeding the CSP login hash, and
// recomputes the login hashes of the affected census participants.
//
// Members imported with surrounding whitespace in name, surname, member number
// or national ID cannot authenticate: the login hash is a byte-exact match, so
// the voter types the clean value and the recomputed hash never matches the one
// stored for them. The service now trims these fields on write, and this command
// repairs the rows written before that.
//
// It defaults to a dry run and only writes when --apply is passed:
//
//	trimmembers --mongoURL "$VOCDONI_MONGOURL" --mongoDB saas          # report only
//	trimmembers --mongoURL "$VOCDONI_MONGOURL" --mongoDB saas --apply  # repair
//	trimmembers ... --org 0xabc… --apply                               # one organization
//
// It is idempotent: once a member is trimmed it no longer matches, so re-running
// is a no-op. It connects to MongoDB directly rather than through db.New, so it
// runs no migrations and honours no VOCDONI_MONGO_RESET_DB.
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

// runTimeout bounds the whole repair. The whitespace prefilter is a regex, which
// no index can serve, so the scan is a collection scan over orgMembers and its
// cost is proportional to the size of the member base.
const runTimeout = 30 * time.Minute

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

	opts := migrations.TrimOptions{Apply: apply}
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
	report, err := migrations.TrimMemberFields(ctx, client.Database(mongoDB), opts)
	if err != nil {
		log.Fatalf("could not trim member fields: %v", err)
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

// printReport summarizes the run. Skipped members are identified by member and
// census ID only: field values are member data and are never printed.
func printReport(report migrations.TrimReport, opts migrations.TrimOptions) {
	apply := opts.Apply
	verb := "would be repaired"
	if apply {
		verb = "repaired"
	}
	log.Infow("member whitespace repair summary",
		"scanned", report.Scanned,
		"membersRepaired", report.Trimmed,
		"participantsRepaired", report.ParticipantsUpdated,
		"censusesAffected", report.CensusesAffected,
		"membersSkipped", report.Skipped,
		"orphanParticipants", report.OrphanParticipants,
		"applied", apply)

	if report.Trimmed == 0 && report.Skipped == 0 {
		log.Info("nothing to do: no member has surrounding whitespace in a login field")
		return
	}
	log.Infow("members "+verb, "count", report.Trimmed, "participants", report.ParticipantsUpdated)

	if report.Skipped > 0 {
		log.Warnw("some members were skipped: trimming them would collide with another "+
			"participant's login hash in the listed census, so they need manual review",
			"count", report.Skipped)
		for _, s := range report.SkippedMembers {
			log.Warnw("skipped member", "memberID", s.MemberID, "censusID", s.CensusID)
		}
	}
	if report.OrphanParticipants > 0 {
		log.Warnw("some participants reference a census that no longer exists; "+
			"their login hashes were left untouched",
			"count", report.OrphanParticipants)
	}
	if !apply {
		log.Info("dry run complete: re-run with --apply to write these changes")
	}
}
