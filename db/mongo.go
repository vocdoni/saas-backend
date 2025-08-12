package db

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"go.mongodb.org/mongo-driver/mongo/readpref"
	"go.mongodb.org/mongo-driver/x/mongo/driver/connstring"
	"go.vocdoni.io/dvote/log"
)

const (
	// connectTimeout is used for connection timeout
	connectTimeout = 10 * time.Second
	// defaultTimeout is used for simple operations (FindOne, UpdateOne, DeleteOne)
	defaultTimeout = 10 * time.Second
	// batchTimeout is used for batch operations (BulkWrite)
	batchTimeout = 20 * time.Second
	// exportTimeout is used for export/import operations (String, Import)
	exportTimeout = 30 * time.Second
)

// MongoStorage uses an external MongoDB service for stoting the user data and election details.
type MongoStorage struct {
	database    string
	DBClient    *mongo.Client
	keysLock    sync.RWMutex
	stripePlans []*Plan

	users               *mongo.Collection
	verifications       *mongo.Collection
	organizations       *mongo.Collection
	organizationInvites *mongo.Collection
	plans               *mongo.Collection
	objects             *mongo.Collection
	orgMembers          *mongo.Collection
	orgMemberGroups     *mongo.Collection
	censusParticipants  *mongo.Collection
	censuses            *mongo.Collection
	publishedCensuses   *mongo.Collection
	processes           *mongo.Collection
	processBundles      *mongo.Collection
	cspTokens           *mongo.Collection
	cspTokensStatus     *mongo.Collection
	jobs                *mongo.Collection
}

type Options struct {
	MongoURL string
	Database string
}

func New(url, database string, plans []*Plan) (*MongoStorage, error) {
	var err error
	ms := &MongoStorage{}
	if url == "" {
		return nil, fmt.Errorf("mongo URL is not defined")
	}
	cs, err := connstring.ParseAndValidate(url)
	if err != nil {
		return nil, fmt.Errorf("cannot parse the connection string: %w", err)
	}
	// set the database name if it is not empty, if it is empty, try to parse it
	// from the URL
	switch {
	case cs.Database == "" && database == "":
		return nil, fmt.Errorf("database name is not defined")
	case database != "":
		cs.Database = database
		ms.database = database
	default:
		ms.database = cs.Database
	}
	// if the auth source is not set, set it to admin (append the param or
	// create it if no other params are present)
	if !cs.AuthSourceSet {
		var sb strings.Builder
		params := "authSource=admin"
		sb.WriteString(url)
		if strings.Contains(url, "?") {
			sb.WriteString("&")
		} else if strings.HasSuffix(url, "/") {
			sb.WriteString("?")
		} else {
			sb.WriteString("/?")
		}
		sb.WriteString(params)
		url = sb.String()
	}
	log.Infow("connecting to mongodb", "url", url)
	// preparing connection
	opts := options.Client()
	opts.ApplyURI(url)
	opts.SetMaxConnecting(200)
	opts.SetConnectTimeout(connectTimeout)
	// create a new client with the connection options
	ctx, cancel := context.WithTimeout(context.Background(), connectTimeout)
	defer cancel()
	client, err := mongo.Connect(ctx, opts)
	if err != nil {
		return nil, fmt.Errorf("cannot connect to mongodb: %w", err)
	}
	// check if the connection is successful
	ctx, cancel2 := context.WithTimeout(context.Background(), connectTimeout)
	defer cancel2()
	// try to ping the database
	if err = client.Ping(ctx, readpref.Primary()); err != nil {
		return nil, fmt.Errorf("cannot ping to mongodb: %w", err)
	}
	// init the database client
	ms.DBClient = client
	if len(plans) > 0 {
		ms.stripePlans = plans
	}
	// init the collections
	if err := ms.initCollections(ms.database); err != nil {
		return nil, err
	}
	// if reset flag is enabled, Reset drops the database documents and recreates indexes
	// else, just init collections and create indexes
	if reset := os.Getenv("VOCDONI_MONGO_RESET_DB"); reset != "" {
		if err := ms.Reset(); err != nil {
			return nil, err
		}
	} else {
		// create indexes
		if err := ms.createIndexes(); err != nil {
			return nil, err
		}
	}
	return ms, nil
}

func (ms *MongoStorage) Close() {
	ctx, cancel := context.WithTimeout(context.Background(), connectTimeout)
	defer cancel()
	if err := ms.DBClient.Disconnect(ctx); err != nil {
		log.Warnw("disconnect error", "error", err)
	}
}

func (ms *MongoStorage) Reset() error {
	log.Infow("resetting database")
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()

	// Drop all collections
	for _, collectionPtr := range ms.collectionsMap() {
		if *collectionPtr != nil {
			if err := (*collectionPtr).Drop(ctx); err != nil {
				return err
			}
		}
	}
	// init the collections
	if err := ms.initCollections(ms.database); err != nil {
		return err
	}

	// create indexes
	return ms.createIndexes()
}

func (ms *MongoStorage) String() string {
	ms.keysLock.RLock()
	defer ms.keysLock.RUnlock()
	// get all users
	ctx, cancel := context.WithTimeout(context.Background(), exportTimeout)
	defer cancel()
	userCur, err := ms.users.Find(ctx, bson.D{{}})
	if err != nil {
		log.Warnw("error decoding user", "error", err)
		return "{}"
	}
	// append all users to the export data
	ctx, cancel2 := context.WithTimeout(context.Background(), exportTimeout)
	defer cancel2()
	var users UserCollection
	for userCur.Next(ctx) {
		var user User
		err := userCur.Decode(&user)
		if err != nil {
			log.Warnw("error finding users", "error", err)
		}
		users.Users = append(users.Users, user)
	}
	// get all user verifications
	ctx, cancel3 := context.WithTimeout(context.Background(), exportTimeout)
	defer cancel3()
	verCur, err := ms.verifications.Find(ctx, bson.D{{}})
	if err != nil {
		log.Warnw("error decoding verification", "error", err)
		return "{}"
	}
	// append all user verifications to the export data
	ctx, cancel4 := context.WithTimeout(context.Background(), exportTimeout)
	defer cancel4()
	var verifications UserVerifications
	for verCur.Next(ctx) {
		var ver UserVerification
		err := verCur.Decode(&ver)
		if err != nil {
			log.Warnw("error finding verifications", "error", err)
		}
		verifications.Verifications = append(verifications.Verifications, ver)
	}
	// get all organizations
	ctx, cancel5 := context.WithTimeout(context.Background(), exportTimeout)
	defer cancel5()
	orgCur, err := ms.organizations.Find(ctx, bson.D{{}})
	if err != nil {
		log.Warnw("error decoding organization", "error", err)
		return "{}"
	}
	// append all organizations to the export data
	ctx, cancel6 := context.WithTimeout(context.Background(), exportTimeout)
	defer cancel6()
	var organizations OrganizationCollection
	for orgCur.Next(ctx) {
		var org Organization
		err := orgCur.Decode(&org)
		if err != nil {
			log.Warnw("error finding organizations", "error", err)
		}
		organizations.Organizations = append(organizations.Organizations, org)
	}

	// get all censuses
	ctx, cancel7 := context.WithTimeout(context.Background(), exportTimeout)
	defer cancel7()
	censusCur, err := ms.censuses.Find(ctx, bson.D{{}})
	if err != nil {
		log.Warnw("error decoding census", "error", err)
		return "{}"
	}
	// append all censuses to the export data
	ctx, cancel8 := context.WithTimeout(context.Background(), exportTimeout)
	defer cancel8()
	var censuses CensusCollection
	for censusCur.Next(ctx) {
		var census Census
		err := censusCur.Decode(&census)
		if err != nil {
			log.Warnw("error finding censuses", "error", err)
		}
		censuses.Censuses = append(censuses.Censuses, census)
	}

	// get all organization members
	ctx, cancel9 := context.WithTimeout(context.Background(), exportTimeout)
	defer cancel9()
	orgMemberCur, err := ms.orgMembers.Find(ctx, bson.D{{}})
	if err != nil {
		log.Warnw("error decoding organization member", "error", err)
		return "{}"
	}
	// append all organization members to the export data
	ctx, cancel10 := context.WithTimeout(context.Background(), exportTimeout)
	defer cancel10()
	var orgMembers OrgMembersCollection
	for orgMemberCur.Next(ctx) {
		var censusPart OrgMember
		err := orgMemberCur.Decode(&censusPart)
		if err != nil {
			log.Warnw("error finding organization members", "error", err)
		}
		orgMembers.OrgMembers = append(orgMembers.OrgMembers, censusPart)
	}

	// get all census participants
	ctx, cancel11 := context.WithTimeout(context.Background(), exportTimeout)
	defer cancel11()
	censusParticipantCur, err := ms.censusParticipants.Find(ctx, bson.D{{}})
	if err != nil {
		log.Warnw("error decoding census participant", "error", err)
		return "{}"
	}
	// append all census participants to the export data
	ctx, cancel12 := context.WithTimeout(context.Background(), exportTimeout)
	defer cancel12()
	var censusParticipants CensusParticipantsCollection
	for censusParticipantCur.Next(ctx) {
		var censusParticipant CensusParticipant
		err := censusParticipantCur.Decode(&censusParticipant)
		if err != nil {
			log.Warnw("error finding census participants", "error", err)
		}
		censusParticipants.CensusParticipants = append(censusParticipants.CensusParticipants, censusParticipant)
	}

	// get all published censuses
	ctx, cancel13 := context.WithTimeout(context.Background(), exportTimeout)
	defer cancel13()
	pubCensusCur, err := ms.publishedCensuses.Find(ctx, bson.D{{}})
	if err != nil {
		log.Warnw("error decoding published census", "error", err)
		return "{}"
	}
	// append all published censuses to the export data
	ctx, cancel14 := context.WithTimeout(context.Background(), exportTimeout)
	defer cancel14()
	var publishedCensuses PublishedCensusesCollection
	for pubCensusCur.Next(ctx) {
		var pubCensus PublishedCensus
		err := pubCensusCur.Decode(&pubCensus)
		if err != nil {
			log.Warnw("error finding published censuses", "error", err)
		}
		publishedCensuses.PublishedCensuses = append(publishedCensuses.PublishedCensuses, pubCensus)
	}

	// get all processes
	ctx, cancel15 := context.WithTimeout(context.Background(), exportTimeout)
	defer cancel15()
	processCur, err := ms.processes.Find(ctx, bson.D{{}})
	if err != nil {
		log.Warnw("error decoding process", "error", err)
		return "{}"
	}
	// append all processes to the export data
	ctx, cancel16 := context.WithTimeout(context.Background(), exportTimeout)
	defer cancel16()
	var processes ProcessesCollection
	for processCur.Next(ctx) {
		var process Process
		err := processCur.Decode(&process)
		if err != nil {
			log.Warnw("error finding processes", "error", err)
		}
		processes.Processes = append(processes.Processes, process)
	}
	// get all organization invites
	ctx, cancel17 := context.WithTimeout(context.Background(), exportTimeout)
	defer cancel17()
	invCur, err := ms.organizationInvites.Find(ctx, bson.D{{}})
	if err != nil {
		log.Warnw("error decoding organization invite", "error", err)
		return "{}"
	}
	// append all organization invites to the export data
	ctx, cancel18 := context.WithTimeout(context.Background(), exportTimeout)
	defer cancel18()
	var organizationInvites OrganizationInvitesCollection
	for invCur.Next(ctx) {
		var inv OrganizationInvite
		err := invCur.Decode(&inv)
		if err != nil {
			log.Warnw("error finding organization invites", "error", err)
		}
		organizationInvites.OrganizationInvites = append(organizationInvites.OrganizationInvites, inv)
	}

	// get all organization groups
	ctx, cancel19 := context.WithTimeout(context.Background(), exportTimeout)
	defer cancel19()
	orgGroupsCursor, err := ms.orgMemberGroups.Find(ctx, bson.D{{}})
	if err != nil {
		log.Warnw("error decoding organization groups", "error", err)
		return "{}"
	}

	// append all organization groups to the export data
	ctx, cancel20 := context.WithTimeout(context.Background(), exportTimeout)
	defer cancel20()
	var orgGroups OrgMemberGroupsCollection
	for orgGroupsCursor.Next(ctx) {
		var orgGroup OrganizationMemberGroup
		err := orgGroupsCursor.Decode(&orgGroup)
		if err != nil {
			log.Warnw("error finding organization groups", "error", err)
		}
		orgGroups.OrgMemberGroups = append(orgGroups.OrgMemberGroups, orgGroup)
	}

	// encode the data to JSON and return it
	data, err := json.Marshal(&Collection{
		users, verifications, organizations, organizationInvites, censuses,
		orgMembers, orgGroups, censusParticipants, publishedCensuses, processes,
	})
	if err != nil {
		log.Warnw("error marshaling data", "error", err)
	}
	return string(data)
}

// Import imports a JSON dataset produced by String() into the database.
func (ms *MongoStorage) Import(jsonData []byte) error {
	ms.keysLock.Lock()
	defer ms.keysLock.Unlock()
	// decode import data
	log.Infow("importing database")
	var collection Collection
	err := json.Unmarshal(jsonData, &collection)
	if err != nil {
		return err
	}
	// create global context to import data
	ctx, cancel := context.WithTimeout(context.Background(), 4*exportTimeout)
	defer cancel()
	// upsert users collection
	log.Infow("importing users", "count", len(collection.Users))
	for _, user := range collection.Users {
		filter := bson.M{"_id": user.ID}
		update := bson.M{"$set": user}
		opts := options.Update().SetUpsert(true)
		_, err := ms.users.UpdateOne(ctx, filter, update, opts)
		if err != nil {
			log.Warnw("error upserting user", "error", err, "user", user.ID)
		}
	}
	// upsert organizations collection
	log.Infow("importing organizations", "count", len(collection.Organizations))
	for _, org := range collection.Organizations {
		filter := bson.M{"_id": org.Address}
		update := bson.M{"$set": org}
		opts := options.Update().SetUpsert(true)
		_, err := ms.organizations.UpdateOne(ctx, filter, update, opts)
		if err != nil {
			log.Warnw("error upserting organization", "error", err, "organization", org.Address)
		}
	}
	log.Infow("imported database")

	// upsert censuses collection
	log.Infow("importing censuses", "count", len(collection.Censuses))
	for _, census := range collection.Censuses {
		filter := bson.M{"_id": census.ID}
		update := bson.M{"$set": census}
		opts := options.Update().SetUpsert(true)
		_, err := ms.censuses.UpdateOne(ctx, filter, update, opts)
		if err != nil {
			log.Warnw("error upserting census", "error", err, "census", census.ID)
		}
	}

	// upsert organization members collection
	log.Infow("importing organization members", "count", len(collection.OrgMembers))
	for _, orgMember := range collection.OrgMembers {
		filter := bson.M{"_id": orgMember.ID}
		update := bson.M{"$set": orgMember}
		opts := options.Update().SetUpsert(true)
		_, err := ms.orgMembers.UpdateOne(ctx, filter, update, opts)
		if err != nil {
			log.Warnw("error upserting organization member", "error", err, "orgMember", orgMember.ID)
		}
	}

	// upsert census participants collection
	log.Infow("importing census participants", "count", len(collection.CensusParticipants))
	for _, censusParticipant := range collection.CensusParticipants {
		filter := bson.M{
			"participantID": censusParticipant.ParticipantID,
			"censusId":      censusParticipant.CensusID,
		}
		update := bson.M{"$set": censusParticipant}
		opts := options.Update().SetUpsert(true)
		_, err := ms.censusParticipants.UpdateOne(ctx, filter, update, opts)
		if err != nil {
			log.Warnw("error upserting census participant", "error", err, "censusParticipant", censusParticipant.ParticipantID)
		}
	}

	// upsert published censuses collection
	log.Infow("importing published censuses", "count", len(collection.PublishedCensuses))
	for _, pubCensus := range collection.PublishedCensuses {
		filter := bson.M{"root": pubCensus.Root, "uri": pubCensus.URI}
		update := bson.M{"$set": pubCensus}
		opts := options.Update().SetUpsert(true)
		_, err := ms.publishedCensuses.UpdateOne(ctx, filter, update, opts)
		if err != nil {
			log.Warnw("error upserting published census", "error", err, "publishedCensus", pubCensus.Root)
		}
	}

	// upsert processes collection
	log.Infow("importing processes", "count", len(collection.Processes))
	for _, process := range collection.Processes {
		filter := bson.M{"_id": process.ID}
		update := bson.M{"$set": process}
		opts := options.Update().SetUpsert(true)
		_, err := ms.processes.UpdateOne(ctx, filter, update, opts)
		if err != nil {
			log.Warnw("error upserting process", "error", err, "process", process.ID.String())
		}
	}
	return nil
}
