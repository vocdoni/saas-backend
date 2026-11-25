package mailtemplates

import (
	"fmt"
	"math"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// entry is one key of a locale file: a string, a plural (one/other) or a list (date.months).
type entry struct {
	raw   string
	msg   *message
	one   *message
	other *message
	list  []string
}

// shape names the kind of an entry, for comparing a locale with English.
func (e *entry) shape() string {
	switch {
	case e.list != nil:
		return "list"
	case e.one != nil:
		return "plural"
	default:
		return "string"
	}
}

// fields lists the {{.Field}} names the entry interpolates.
func (e *entry) fields() []string {
	var out []string
	for _, m := range []*message{e.msg, e.one, e.other} {
		if m != nil {
			out = append(out, m.fields...)
		}
	}
	return out
}

// locale is the parsed i18n/<lang>.yaml, flattened to dotted keys ("shared.button_fallback",
// "verification_account.subject", "duration.minute").
type locale struct {
	lang    string
	entries map[string]*entry
}

// parseLocale parses and flattens one locale file.
func parseLocale(lang string, data []byte) (*locale, error) {
	var tree map[string]any
	if err := yaml.Unmarshal(data, &tree); err != nil {
		return nil, fmt.Errorf("could not parse i18n/%s.yaml: %w", lang, err)
	}
	loc := &locale{lang: lang, entries: map[string]*entry{}}
	for key, value := range tree {
		if err := loc.flatten(key, value); err != nil {
			return nil, fmt.Errorf("i18n/%s.yaml: %w", lang, err)
		}
	}
	return loc, nil
}

func (loc *locale) flatten(key string, value any) error {
	switch v := value.(type) {
	case nil:
		return nil // an empty section, e.g. a locale not translated yet
	case string:
		m, err := parseMessage(v)
		if err != nil {
			return fmt.Errorf("%s: %w", key, err)
		}
		loc.entries[key] = &entry{raw: v, msg: m}
	case []any:
		list := make([]string, 0, len(v))
		for _, item := range v {
			s, ok := item.(string)
			if !ok {
				return fmt.Errorf("%s: lists hold strings only", key)
			}
			list = append(list, s)
		}
		loc.entries[key] = &entry{list: list}
	case map[string]any:
		if isPlural(v) {
			one, err := parseMessage(v["one"].(string))
			if err != nil {
				return fmt.Errorf("%s.one: %w", key, err)
			}
			other, err := parseMessage(v["other"].(string))
			if err != nil {
				return fmt.Errorf("%s.other: %w", key, err)
			}
			loc.entries[key] = &entry{one: one, other: other}
			return nil
		}
		for sub, subValue := range v {
			if err := loc.flatten(key+"."+sub, subValue); err != nil {
				return err
			}
		}
	default:
		return fmt.Errorf("%s: unsupported value %v", key, v)
	}
	return nil
}

// isPlural reports whether a map is a plural entry: exactly the string keys one and other.
func isPlural(m map[string]any) bool {
	one, okOne := m["one"].(string)
	other, okOther := m["other"].(string)
	return len(m) == 2 && okOne && okOther && one != "" && other != ""
}

// checkAgainst validates a locale against English: every key it has must exist in English with
// the same shape, interpolate only fields the English string uses, and lists keep their length.
// Missing keys are allowed here (they fall back to English); TestLocalesComplete reports them.
func (loc *locale) checkAgainst(en *locale) error {
	for key, e := range loc.entries {
		ref, ok := en.entries[key]
		if !ok {
			continue // an extra key is never used; TestLocalesComplete reports it
		}
		if e.shape() != ref.shape() {
			return fmt.Errorf("i18n/%s.yaml: %s is a %s, English has a %s", loc.lang, key, e.shape(), ref.shape())
		}
		for _, f := range e.fields() {
			if !slices.Contains(ref.fields(), f) {
				return fmt.Errorf("i18n/%s.yaml: %s uses {{.%s}}, which the English string does not", loc.lang, key, f)
			}
		}
		if e.list != nil && len(e.list) != len(ref.list) {
			return fmt.Errorf("i18n/%s.yaml: %s has %d items, English has %d", loc.lang, key, len(e.list), len(ref.list))
		}
	}
	return nil
}

// formatter renders values in one language, falling back to English per key.
type formatter struct {
	loc, en *locale
	zone    *time.Location
}

// lookup returns the entry for key in the language, else in English.
func (f formatter) lookup(key string) (*entry, error) {
	if e, ok := f.loc.entries[key]; ok {
		return e, nil
	}
	if e, ok := f.en.entries[key]; ok {
		return e, nil
	}
	return nil, fmt.Errorf("missing string %q", key)
}

// value formats a template value for the reader: numbers grouped, durations and dates in words.
func (f formatter) value(v any) (string, error) {
	switch t := v.(type) {
	case nil:
		return "", nil
	case string:
		return t, nil
	case time.Duration:
		return f.duration(t)
	case time.Time:
		return f.date(t)
	case fmt.Stringer:
		return t.String(), nil
	}
	if n, ok := asInt(v); ok {
		return f.number(n)
	}
	return fmt.Sprint(v), nil
}

// duration writes d in whole minutes, hours or days, rounding up and never below one minute.
func (f formatter) duration(d time.Duration) (string, error) {
	unit, n := "minute", ceilDiv(d, time.Minute)
	if n >= 60 {
		unit, n = "hour", ceilDiv(d, time.Hour)
	}
	if unit == "hour" && n >= 24 {
		unit, n = "day", ceilDiv(d, 24*time.Hour)
	}
	e, err := f.lookup("duration." + unit)
	if err != nil {
		return "", err
	}
	count, err := f.number(n)
	if err != nil {
		return "", err
	}
	return pick(e, n).render(asText, fieldValues(map[string]string{"N": count}))
}

// date writes the calendar date of t in the fixed Europe/Madrid zone, without the time.
func (f formatter) date(t time.Time) (string, error) {
	t = t.In(f.zone)
	months, err := f.lookup("date.months")
	if err != nil {
		return "", err
	}
	format, err := f.lookup("date.format")
	if err != nil {
		return "", err
	}
	return format.msg.render(asText, fieldValues(map[string]string{
		"Day":   strconv.Itoa(t.Day()),
		"Month": months.list[t.Month()-1],
		"Year":  strconv.Itoa(t.Year()),
	}))
}

// number writes n with the locale's thousands separator (number.group), applying grouping only
// when the digit count is at least 3+minGrouping (number.min_grouping). With minGrouping=1 a
// four-digit number like 1248 is grouped; with minGrouping=2 it is not, and five digits are
// needed (12480 → "12.480").
func (f formatter) number(n int64) (string, error) {
	group, err := f.lookup("number.group")
	if err != nil {
		return "", err
	}
	minGroupEntry, err := f.lookup("number.min_grouping")
	if err != nil {
		return "", err
	}
	minGroup, err := strconv.Atoi(minGroupEntry.raw)
	if err != nil || minGroup < 1 {
		return "", fmt.Errorf("number.min_grouping: expected a positive integer, got %q", minGroupEntry.raw)
	}
	digits := strconv.FormatInt(n, 10)
	sign := ""
	if n < 0 {
		sign, digits = "-", digits[1:]
	}
	applyGrouping := len(digits) >= 3+minGroup
	var out strings.Builder
	for i, d := range digits {
		if applyGrouping && i > 0 && (len(digits)-i)%3 == 0 {
			out.WriteString(group.raw)
		}
		out.WriteString(string(d))
	}
	return sign + out.String(), nil
}

// pick returns the plural form of e for n.
func pick(e *entry, n int64) *message {
	if n == 1 {
		return e.one
	}
	return e.other
}

func fieldValues(values map[string]string) func(string) (string, error) {
	return func(field string) (string, error) {
		v, ok := values[field]
		if !ok {
			return "", fmt.Errorf("unknown field %q", field)
		}
		return v, nil
	}
}

func ceilDiv(d, unit time.Duration) int64 {
	return max(1, int64(math.Ceil(float64(d)/float64(unit))))
}

// asInt converts any integer kind to int64.
func asInt(v any) (int64, bool) {
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return rv.Int(), true
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return int64(rv.Uint()), true //nolint:gosec // counts never overflow int64
	default:
		return 0, false
	}
}
