package internal

import (
	"time"

	"go.mongodb.org/mongo-driver/bson/primitive"
)

// ObjectID is a wrapper over primitive.ObjectID, with a simpler String method
type ObjectID primitive.ObjectID

// NilObjectID is the zero value for ObjectID.
var NilObjectID ObjectID

// NewObjectID generates a new ObjectID.
func NewObjectID() ObjectID { return ObjectID(primitive.NewObjectID()) }

// String returns the hex encoding of the ObjectID (exactly the same as id.Hex())
func (id ObjectID) String() string { return id.Hex() }

// // Wrappers over primitive.ObjectID // //

// Hex returns the hex encoding of the ObjectID as a string.
func (id ObjectID) Hex() string { return primitive.ObjectID(id).Hex() }

// IsZero returns true if id is the empty ObjectID.
func (id ObjectID) IsZero() bool { return primitive.ObjectID(id).IsZero() }

// Timestamp extracts the time part of the ObjectId.
func (id ObjectID) Timestamp() (t time.Time) { return primitive.ObjectID(id).Timestamp() }

// MarshalJSON returns the ObjectID as a string
func (id ObjectID) MarshalJSON() ([]byte, error) { return primitive.ObjectID(id).MarshalJSON() }

// UnmarshalJSON populates the byte slice with the ObjectID. If the byte slice is 24 bytes long, it
// will be populated with the hex representation of the ObjectID. If the byte slice is twelve bytes
// long, it will be populated with the BSON representation of the ObjectID. This method also accepts empty strings and
// decodes them as NilObjectID. For any other inputs, an error will be returned.
func (id *ObjectID) UnmarshalJSON(b []byte) error { return (*primitive.ObjectID)(id).UnmarshalJSON(b) }

// MarshalText returns the ObjectID as UTF-8-encoded text. Implementing this allows us to use ObjectID
// as a map key when marshalling JSON. See https://pkg.go.dev/encoding#TextMarshaler
func (id ObjectID) MarshalText() ([]byte, error) { return primitive.ObjectID(id).MarshalText() }

// UnmarshalText populates the byte slice with the ObjectID. Implementing this allows us to use ObjectID
// as a map key when unmarshalling JSON. See https://pkg.go.dev/encoding#TextUnmarshaler
func (id *ObjectID) UnmarshalText(b []byte) error { return (*primitive.ObjectID)(id).UnmarshalText(b) }

// ObjectIDFromHex creates a new ObjectID from a hex string. It returns an error if the hex string is not a
// valid ObjectID.
func ObjectIDFromHex(s string) (ObjectID, error) {
	id, err := primitive.ObjectIDFromHex(s)
	return ObjectID(id), err
}
