package internal

import (
	"time"

	"go.mongodb.org/mongo-driver/bson/primitive"
)

// ObjectID is simply an primitive.ObjectID but with a simpler String method
type ObjectID primitive.ObjectID

// NilObjectID is the zero value for ObjectID.
var NilObjectID ObjectID

func NewObjectID() ObjectID        { return ObjectID(primitive.NewObjectID()) }
func (id ObjectID) String() string { return id.Hex() }

// Wrappers over primitive.ObjectID

func (id ObjectID) Hex() string                   { return primitive.ObjectID(id).Hex() }
func (id ObjectID) IsZero() bool                  { return primitive.ObjectID(id).IsZero() }
func (id ObjectID) Timestamp() (t time.Time)      { return primitive.ObjectID(id).Timestamp() }
func (id ObjectID) MarshalJSON() ([]byte, error)  { return primitive.ObjectID(id).MarshalJSON() }
func (id *ObjectID) UnmarshalJSON(b []byte) error { return (*primitive.ObjectID)(id).UnmarshalJSON(b) }
func (id ObjectID) MarshalText() ([]byte, error)  { return primitive.ObjectID(id).MarshalText() }
func (id *ObjectID) UnmarshalText(b []byte) error { return (*primitive.ObjectID)(id).UnmarshalText(b) }
func ObjectIDFromHex(s string) (ObjectID, error) {
	id, err := primitive.ObjectIDFromHex(s)
	return ObjectID(id), err
}
