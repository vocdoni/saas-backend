package db

import (
	"fmt"

	"go.mongodb.org/mongo-driver/v2/bson"
)

// UntypedDoc is a schemaless BSON subdocument (organization meta, member "other"
// fields, election metadata) held as plain Go values.
//
// It exists because the driver hands untyped documents back as its own named
// types — bson.M when the client sets DefaultDocumentM (as ours does, see
// db/mongo.go), bson.D otherwise, bson.A for arrays. A Go type switch matches
// exact types, not underlying types, so `case map[string]any:` silently misses a
// bson.M and every consumer has to learn about driver types on its own. Rather
// than repeating that per consumer, UnmarshalBSON normalizes them away once,
// here at the decode boundary: what reaches the rest of the codebase is
// map[string]any, []any and scalars, at every depth.
//
// The underlying type is map[string]any, so assignments to and from plain maps,
// maps.Copy, JSON (un)marshalling and $set writes all keep working unchanged.
type UntypedDoc map[string]any

// UnmarshalBSON implements bson.Unmarshaler.
func (d *UntypedDoc) UnmarshalBSON(data []byte) error {
	// a BSON null reaches us as an empty payload: the driver only short-circuits
	// null for pointer targets, and bson.Unmarshal rejects an empty document.
	if len(data) == 0 {
		*d = nil
		return nil
	}
	var m bson.M
	if err := bson.Unmarshal(data, &m); err != nil {
		return fmt.Errorf("decoding untyped document: %w", err)
	}
	// bson.Unmarshal uses the default registry regardless of the client options,
	// so nested documents arrive as bson.D here even though the collection read
	// that produced data used DefaultDocumentM. normalizeUntyped handles both.
	*d = normalizeMap(m)
	return nil
}

// normalizeUntyped recursively replaces the driver's document and array types
// with plain map[string]any and []any. Scalars are returned untouched.
func normalizeUntyped(v any) any {
	switch t := v.(type) {
	case bson.M:
		// same underlying type: convert instead of copying, then normalize the
		// values in place (assigning to existing keys while ranging is safe)
		return normalizeMap(t)
	case map[string]any:
		return normalizeMap(t)
	case bson.D:
		m := make(map[string]any, len(t))
		for _, e := range t {
			m[e.Key] = normalizeUntyped(e.Value)
		}
		return m
	case bson.A:
		return normalizeSlice(t)
	case []any:
		return normalizeSlice(t)
	default:
		return v
	}
}

func normalizeMap(m map[string]any) map[string]any {
	for k, v := range m {
		m[k] = normalizeUntyped(v)
	}
	return m
}

func normalizeSlice(a []any) []any {
	for i, v := range a {
		a[i] = normalizeUntyped(v)
	}
	return a
}
