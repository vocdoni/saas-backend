package db

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

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
	orgParticipants     *mongo.Collection
	censusMemberships   *mongo.Collection
	censuses            *mongo.Collection
	publishedCensuses   *mongo.Collection
	processes           *mongo.Collection
	processBundles      *mongo.Collection
	cspTokens           *mongo.Collection
	cspTokensStatus     *mongo.Collection
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
