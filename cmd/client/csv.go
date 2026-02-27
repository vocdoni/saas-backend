package main

import (
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

// csvRow stores one parsed input row and normalized values by known field.
type csvRow struct {
	Line       int
	Identifier string
	Values     map[string]string
}

var csvFieldByHeader = map[string]string{
	"name":         "name",
	"surname":      "surname",
	"birthdate":    "birthDate",
	"email":        "email",
	"phone":        "phone",
	"password":     "password",
	"weight":       "weight",
	"nationalid":   "nationalId",
	"membernumber": "memberNumber",
}

// readCSV reads a CSV file, maps only supported member columns and returns rows
// ready for member processing.
func readCSV(path, identifierField string) ([]csvRow, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open CSV %s: %w", path, err)
	}
	defer func() {
		if closeErr := file.Close(); closeErr != nil {
			fmt.Printf("warning: close CSV file %s: %v\n", path, closeErr)
		}
	}()

	reader := csv.NewReader(file)
	reader.FieldsPerRecord = -1

	headers, err := reader.Read()
	if err != nil {
		if errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("read CSV header: empty file")
		}
		return nil, fmt.Errorf("read CSV header: %w", err)
	}

	indices := map[string]int{}
	for idx, header := range headers {
		canonical, ok := canonicalCSVField(header)
		if !ok {
			continue
		}
		indices[canonical] = idx
	}

	if _, ok := indices[identifierField]; !ok {
		return nil, fmt.Errorf("CSV header missing required identifier column %q", identifierField)
	}

	rows := make([]csvRow, 0)
	line := 1
	for {
		record, readErr := reader.Read()
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				break
			}
			return nil, fmt.Errorf("read CSV line %d: %w", line+1, readErr)
		}
		line++

		if isBlankCSVRow(record) {
			continue
		}

		values := map[string]string{}
		for field, idx := range indices {
			if idx >= len(record) {
				continue
			}
			value := strings.TrimSpace(record[idx])
			if value == "" {
				continue
			}
			values[field] = value
		}

		rows = append(rows, csvRow{
			Line:       line,
			Identifier: csvValue(record, indices[identifierField]),
			Values:     values,
		})
	}

	return rows, nil
}

func canonicalCSVField(raw string) (string, bool) {
	field, ok := csvFieldByHeader[strings.ToLower(strings.TrimSpace(raw))]
	return field, ok
}

func isBlankCSVRow(record []string) bool {
	for _, value := range record {
		if strings.TrimSpace(value) != "" {
			return false
		}
	}
	return true
}

func csvValue(record []string, index int) string {
	if index < 0 || index >= len(record) {
		return ""
	}
	return strings.TrimSpace(record[index])
}
