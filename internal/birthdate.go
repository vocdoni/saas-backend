package internal

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// ParseBirthDate normalizes the birth date into YYYY-MM-DD while allowing:
//   - ISO-like dates with flexible separators (YYYY-MM-DD, YYYY/MM/DD, YYYY MM DD)
//   - Day-first dates with flexible separators (DD/MM/YYYY, DD-MM-YYYY, DD MM YYYY)
func ParseBirthDate(value string) (time.Time, string, error) {
	dateStr := strings.TrimSpace(value)
	if dateStr == "" {
		return time.Time{}, "", fmt.Errorf("invalid birthdate: empty")
	}

	parts := splitDateParts(dateStr)
	if len(parts) != 3 {
		return time.Time{}, "", fmt.Errorf("invalid birthdate format: %s", value)
	}

	numbers := make([]int, 3)
	for i, p := range parts {
		v, err := strconv.Atoi(p)
		if err != nil {
			return time.Time{}, "", fmt.Errorf("invalid birthdate format: %s", value)
		}
		numbers[i] = v
	}

	var year, month, day int
	switch {
	// Year-first format (YYYY-..-..)
	case len(parts[0]) == 4:
		year, month, day = numbers[0], numbers[1], numbers[2]
	// Day-first format (DD-..-YYYY)
	case len(parts[2]) == 4:
		day, month, year = numbers[0], numbers[1], numbers[2]
	default:
		return time.Time{}, "", fmt.Errorf("invalid birthdate format: %s", value)
	}

	parsed := time.Date(year, time.Month(month), day, 0, 0, 0, 0, time.UTC)
	if parsed.Year() != year || int(parsed.Month()) != month || parsed.Day() != day {
		return time.Time{}, "", fmt.Errorf("invalid birthdate format: %s", value)
	}

	return parsed, parsed.Format(time.DateOnly), nil
}

func splitDateParts(value string) []string {
	// Split on any non-digit separator; keep multiple separators flexible.
	return strings.FieldsFunc(value, func(r rune) bool {
		return r < '0' || r > '9'
	})
}
