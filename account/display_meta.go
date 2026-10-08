package account

import (
	"maps"
	"math"

	"go.mongodb.org/mongo-driver/v2/bson"
)

// questionChoicesMetaKey is the key of a question's free-form metadata that holds the display
// info of its choices: an array of entries joined to the choices by their value.
const questionChoicesMetaKey = "choices"

// QuestionDisplayMeta splits a question's free-form metadata (db.VotingProcessQuestion.Metadata)
// into what its election document carries: the question-level part (every key but "choices"),
// written as questions[0].meta, and each choice's entry of "choices" keyed by its value, minus the
// value itself (description, image and any other key verbatim), written as that choice's meta.
// An entry without a usable value is dropped. Either result is nil when there is nothing in it.
func QuestionDisplayMeta(metadata map[string]any) (questionMeta map[string]any, choicesMeta map[uint32]map[string]any) {
	for key, value := range metadata {
		if key != questionChoicesMetaKey {
			if questionMeta == nil {
				questionMeta = make(map[string]any)
			}
			questionMeta[key] = value
			continue
		}
		entries, ok := asSlice(value)
		if !ok {
			continue
		}
		for _, raw := range entries {
			entry, ok := asMap(raw)
			if !ok {
				continue
			}
			choiceValue, ok := asChoiceValue(entry["value"])
			if !ok {
				continue
			}
			rest := maps.Clone(entry)
			delete(rest, "value")
			if len(rest) == 0 {
				continue
			}
			if choicesMeta == nil {
				choicesMeta = make(map[uint32]map[string]any)
			}
			choicesMeta[choiceValue] = rest
		}
	}
	return questionMeta, choicesMeta
}

// ChoiceImageURLs returns the image URLs of a choice's display info: its "image" as a plain URL,
// or the "default" and "thumbnail" URLs of an image object.
func ChoiceImageURLs(choiceMeta map[string]any) []string {
	var urls []string
	switch image := choiceMeta["image"].(type) {
	case string:
		if image != "" {
			urls = append(urls, image)
		}
	default:
		obj, ok := asMap(image)
		if !ok {
			return nil
		}
		for _, key := range []string{"default", "thumbnail"} {
			if u, ok := obj[key].(string); ok && u != "" {
				urls = append(urls, u)
			}
		}
	}
	return urls
}

// ChoicesMetaEntries returns the entries of a question metadata's "choices" array that are
// documents, as the maps stored in it (changing one changes the metadata).
func ChoicesMetaEntries(metadata map[string]any) []map[string]any {
	raw, ok := asSlice(metadata[questionChoicesMetaKey])
	if !ok {
		return nil
	}
	entries := make([]map[string]any, 0, len(raw))
	for _, item := range raw {
		if entry, ok := asMap(item); ok {
			entries = append(entries, entry)
		}
	}
	return entries
}

// ChoiceMetaValue returns the choice value a "choices" metadata entry is joined to its choice by.
func ChoiceMetaValue(entry map[string]any) (uint32, bool) {
	return asChoiceValue(entry["value"])
}

// AsMap reads a decoded document, whether it came from JSON or from Mongo, as the same map.
func AsMap(v any) (map[string]any, bool) {
	return asMap(v)
}

// asMap reads a decoded document, whether it came from JSON or from Mongo.
func asMap(v any) (map[string]any, bool) {
	switch m := v.(type) {
	case map[string]any:
		return m, true
	case bson.M:
		return map[string]any(m), true
	default:
		return nil, false
	}
}

// asSlice reads a decoded array, whether it came from JSON or from Mongo.
func asSlice(v any) ([]any, bool) {
	switch s := v.(type) {
	case []any:
		return s, true
	case bson.A:
		return []any(s), true
	default:
		return nil, false
	}
}

// asChoiceValue reads a choice value decoded from JSON (float64) or Mongo (int32/int64/float64).
func asChoiceValue(v any) (uint32, bool) {
	var f float64
	switch n := v.(type) {
	case int32:
		f = float64(n)
	case int64:
		f = float64(n)
	case int:
		f = float64(n)
	case float64:
		f = n
	default:
		return 0, false
	}
	if f < 0 || f > math.MaxUint32 || f != math.Trunc(f) {
		return 0, false
	}
	return uint32(f), true
}
