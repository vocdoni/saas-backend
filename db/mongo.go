package db

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"go.mongodb.org/mongo-driver/mongo/readpref"
	"go.vocdoni.io/dvote/log"
)

// MongoStorage uses an external MongoDB service for stoting the user data and election details.
type MongoStorage struct {
	database    string
	client      *mongo.Client
	keysLock    sync.RWMutex
	stripePlans []*Plan

	users               *mongo.Collection
	verifications       *mongo.Collection
	organizations       *mongo.Collection
	organizationInvites *mongo.Collection
	plans               *mongo.Collection
	objects             *mongo.Collection
	orgParticipants     *mongo.Collection
	censusMemberships   *mongo.Collection
	censuses            *mongo.Collection
	publishedCensuses   *mongo.Collection
	processes           *mongo.Collection
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
	if database == "" {
		return nil, fmt.Errorf("mongo database is not defined")
	}
	log.Infow("connecting to mongodb", "url", url, "database", database)
	// preparing connection
	opts := options.Client()
	opts.ApplyURI(url)
	opts.SetMaxConnecting(200)
	timeout := time.Second * 10
	opts.ConnectTimeout = &timeout
	// create a new client with the connection options
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	client, err := mongo.Connect(ctx, opts)
	if err != nil {
		return nil, fmt.Errorf("cannot connect to mongodb: %w", err)
	}
	// check if the connection is successful
	ctx, cancel2 := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel2()
	err = client.Ping(ctx, readpref.Primary())
	if err != nil {
		return nil, fmt.Errorf("cannot connect to mongodb: %w", err)
	}
	// init the database client
	ms.client = client
	ms.database = database
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
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := ms.client.Disconnect(ctx); err != nil {
		log.Warn(err)
	}
}

func (ms *MongoStorage) Reset() error {
	log.Infof("resetting database")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	// drop users collection
	if err := ms.users.Drop(ctx); err != nil {
		return err
	}
	// drop organizations collection
	if err := ms.organizations.Drop(ctx); err != nil {
		return err
	}
	// drop organizationInvites collection
	if err := ms.organizationInvites.Drop(ctx); err != nil {
		return err
	}
	// drop verifications collection
	if err := ms.verifications.Drop(ctx); err != nil {
		return err
	}
	// drop subscriptions collection
	if err := ms.plans.Drop(ctx); err != nil {
		return err
	}
	// drop the objects collection
	if err := ms.objects.Drop(ctx); err != nil {
		return err
	}
	// drop the  orgParticipants collection
	if err := ms.orgParticipants.Drop(ctx); err != nil {
		return err
	}
	// drop the censusMemberships collection
	if err := ms.censusMemberships.Drop(ctx); err != nil {
		return err
	}
	// drop the censuses collection
	if err := ms.censuses.Drop(ctx); err != nil {
		return err
	}
	// drop the publishedCensuses collection
	if err := ms.publishedCensuses.Drop(ctx); err != nil {
		return err
	}
	// drop the processes collection
	if err := ms.processes.Drop(ctx); err != nil {
		return err
	}
	// init the collections
	if err := ms.initCollections(ms.database); err != nil {
		return err
	}
	// create indexes
	if err := ms.createIndexes(); err != nil {
		return err
	}
	return nil
}

func (ms *MongoStorage) String() string {
	const contextTimeout = 30 * time.Second
	ms.keysLock.RLock()
	defer ms.keysLock.RUnlock()
	// get all users
	ctx, cancel := context.WithTimeout(context.Background(), contextTimeout)
	defer cancel()
	userCur, err := ms.users.Find(ctx, bson.D{{}})
	if err != nil {
		log.Warn(err)
		return "{}"
	}
	// append all users to the export data
	ctx, cancel2 := context.WithTimeout(context.Background(), contextTimeout)
	defer cancel2()
	var users UserCollection
	for userCur.Next(ctx) {
		var user User
		err := userCur.Decode(&user)
		if err != nil {
			log.Warn(err)
		}
		users.Users = append(users.Users, user)
	}
	// get all user verifications
	ctx, cancel3 := context.WithTimeout(context.Background(), contextTimeout)
	defer cancel3()
	verCur, err := ms.verifications.Find(ctx, bson.D{{}})
	if err != nil {
		log.Warn(err)
		return "{}"
	}
	// append all user verifications to the export data
	ctx, cancel4 := context.WithTimeout(context.Background(), contextTimeout)
	defer cancel4()
	var verifications UserVerifications
	for verCur.Next(ctx) {
		var ver UserVerification
		err := verCur.Decode(&ver)
		if err != nil {
			log.Warn(err)
		}
		verifications.Verifications = append(verifications.Verifications, ver)
	}
	// get all organizations
	ctx, cancel5 := context.WithTimeout(context.Background(), contextTimeout)
	defer cancel5()
	orgCur, err := ms.organizations.Find(ctx, bson.D{{}})
	if err != nil {
		log.Warn(err)
		return "{}"
	}
	// append all organizations to the export data
	ctx, cancel6 := context.WithTimeout(context.Background(), contextTimeout)
	defer cancel6()
	var organizations OrganizationCollection
	for orgCur.Next(ctx) {
		var org Organization
		err := orgCur.Decode(&org)
		if err != nil {
			log.Warn(err)
		}
		organizations.Organizations = append(organizations.Organizations, org)
	}

	// get all censuses
	ctx, cancel7 := context.WithTimeout(context.Background(), contextTimeout)
	defer cancel7()
	censusCur, err := ms.censuses.Find(ctx, bson.D{{}})
	if err != nil {
		log.Warn(err)
		return "{}"
	}
	// append all censuses to the export data
	ctx, cancel8 := context.WithTimeout(context.Background(), contextTimeout)
	defer cancel8()
	var censuses CensusCollection
	for censusCur.Next(ctx) {
		var census Census
		err := censusCur.Decode(&census)
		if err != nil {
			log.Warn(err)
		}
		censuses.Censuses = append(censuses.Censuses, census)
	}

	// get all census participants
	ctx, cancel9 := context.WithTimeout(context.Background(), contextTimeout)
	defer cancel9()
	censusPartCur, err := ms.orgParticipants.Find(ctx, bson.D{{}})
	if err != nil {
		log.Warn(err)
		return "{}"
	}
	// append all census participants to the export data
	ctx, cancel10 := context.WithTimeout(context.Background(), contextTimeout)
	defer cancel10()
	var orgParticipants OrgParticipantsCollection
	for censusPartCur.Next(ctx) {
		var censusPart OrgParticipant
		err := censusPartCur.Decode(&censusPart)
		if err != nil {
			log.Warn(err)
		}
		orgParticipants.OrgParticipants = append(orgParticipants.OrgParticipants, censusPart)
	}

	// get all census memberships
	ctx, cancel11 := context.WithTimeout(context.Background(), contextTimeout)
	defer cancel11()
	censusMemCur, err := ms.censusMemberships.Find(ctx, bson.D{{}})
	if err != nil {
		log.Warn(err)
		return "{}"
	}
	// append all census memberships to the export data
	ctx, cancel12 := context.WithTimeout(context.Background(), contextTimeout)
	defer cancel12()
	var censusMemberships CensusMembershipsCollection
	for censusMemCur.Next(ctx) {
		var censusMem CensusMembership
		err := censusMemCur.Decode(&censusMem)
		if err != nil {
			log.Warn(err)
		}
		censusMemberships.CensusMemberships = append(censusMemberships.CensusMemberships, censusMem)
	}

	// get all published censuses
	ctx, cancel13 := context.WithTimeout(context.Background(), contextTimeout)
	defer cancel13()
	pubCensusCur, err := ms.publishedCensuses.Find(ctx, bson.D{{}})
	if err != nil {
		log.Warn(err)
		return "{}"
	}
	// append all published censuses to the export data
	ctx, cancel14 := context.WithTimeout(context.Background(), contextTimeout)
	defer cancel14()
	var publishedCensuses PublishedCensusesCollection
	for pubCensusCur.Next(ctx) {
		var pubCensus PublishedCensus
		err := pubCensusCur.Decode(&pubCensus)
		if err != nil {
			log.Warn(err)
		}
		publishedCensuses.PublishedCensuses = append(publishedCensuses.PublishedCensuses, pubCensus)
	}

	// get all processes
	ctx, cancel15 := context.WithTimeout(context.Background(), contextTimeout)
	defer cancel15()
	processCur, err := ms.processes.Find(ctx, bson.D{{}})
	if err != nil {
		log.Warn(err)
		return "{}"
	}
	// append all processes to the export data
	ctx, cancel16 := context.WithTimeout(context.Background(), contextTimeout)
	defer cancel16()
	var processes ProcessesCollection
	for processCur.Next(ctx) {
		var process Process
		err := processCur.Decode(&process)
		if err != nil {
			log.Warn(err)
		}
		processes.Processes = append(processes.Processes, process)
	}
	// get all organization invites
	ctx, cancel17 := context.WithTimeout(context.Background(), contextTimeout)
	defer cancel17()
	invCur, err := ms.organizationInvites.Find(ctx, bson.D{{}})
	if err != nil {
		log.Warn(err)
		return "{}"
	}
	// append all organization invites to the export data
	ctx, cancel18 := context.WithTimeout(context.Background(), contextTimeout)
	defer cancel18()
	var organizationInvites OrganizationInvitesCollection
	for invCur.Next(ctx) {
		var inv OrganizationInvite
		err := invCur.Decode(&inv)
		if err != nil {
			log.Warn(err)
		}
		organizationInvites.OrganizationInvites = append(organizationInvites.OrganizationInvites, inv)
	}

	// encode the data to JSON and return it
	data, err := json.Marshal(&Collection{
		users, verifications, organizations, organizationInvites, censuses,
		orgParticipants, censusMemberships, publishedCensuses, processes,
	})
	if err != nil {
		log.Warn(err)
	}
	return string(data)
}

// Import imports a JSON dataset produced by String() into the database.
func (ms *MongoStorage) Import(jsonData []byte) error {
	ms.keysLock.RLock()
	defer ms.keysLock.RUnlock()
	// decode import data
	log.Infof("importing database")
	var collection Collection
	err := json.Unmarshal(jsonData, &collection)
	if err != nil {
		return err
	}
	// create global context to import data
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	// upsert users collection
	log.Infow("importing users", "count", len(collection.Users))
	for _, user := range collection.Users {
		filter := bson.M{"_id": user.ID}
		update := bson.M{"$set": user}
		opts := options.Update().SetUpsert(true)
		_, err := ms.users.UpdateOne(ctx, filter, update, opts)
		if err != nil {
			log.Warnw("error upserting user", "err", err, "user", user.ID)
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
			log.Warnw("error upserting organization", "err", err, "organization", org.Address)
		}
	}
	log.Infof("imported database!")

	// upsert censuses collection
	log.Infow("importing censuses", "count", len(collection.Censuses))
	for _, census := range collection.Censuses {
		filter := bson.M{"_id": census.ID}
		update := bson.M{"$set": census}
		opts := options.Update().SetUpsert(true)
		_, err := ms.censuses.UpdateOne(ctx, filter, update, opts)
		if err != nil {
			log.Warnw("error upserting census", "err", err, "census", census.ID)
		}
	}

	// upsert census participants collection
	log.Infow("importing census participants", "count", len(collection.OrgParticipants))
	for _, censusPart := range collection.OrgParticipants {
		filter := bson.M{"_id": censusPart.ID}
		update := bson.M{"$set": censusPart}
		opts := options.Update().SetUpsert(true)
		_, err := ms.orgParticipants.UpdateOne(ctx, filter, update, opts)
		if err != nil {
			log.Warnw("error upserting census participant", "err", err, "orgParticipant", censusPart.ID)
		}
	}

	// upsert census memberships collection
	log.Infow("importing census memberships", "count", len(collection.CensusMemberships))
	for _, censusMem := range collection.CensusMemberships {
		filter := bson.M{
			"participantNo": censusMem.ParticipantNo,
			"censusId":      censusMem.CensusID,
		}
		update := bson.M{"$set": censusMem}
		opts := options.Update().SetUpsert(true)
		_, err := ms.censusMemberships.UpdateOne(ctx, filter, update, opts)
		if err != nil {
			log.Warnw("error upserting census membership", "err", err, "censusMembership", censusMem.ParticipantNo)
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
			log.Warnw("error upserting published census", "err", err, "publishedCensus", pubCensus.Root)
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
			log.Warnw("error upserting process", "err", err, "process", process.ID)
		}
	}
	return nil
}
