#!/bin/bash
set -e

# Wait for MongoDB to start
until mongo --host localhost -u root -p vocdoni --authenticationDatabase admin --eval "print(\"waited for connection\")"
do
    echo "Waiting for MongoDB to start..."
    sleep 1
done

# Initialize replica set
mongo --host localhost -u root -p vocdoni --authenticationDatabase admin <<EOF
    rs.initiate({
        _id: "rs0",
        members: [
            { _id: 0, host: "mongo:27017" }
        ]
    })
EOF

echo "MongoDB Replica Set initialized"
