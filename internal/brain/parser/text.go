package parser

import (
	"bytes"
	"encoding/csv"
	"io"
	"strings"
)

// ParseText reads raw text content from a reader and returns it in a single-element slice.
func ParseText(r io.Reader) ([]string, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	return []string{string(data)}, nil
}

// ParseCSV reads a CSV file from a reader, formatting each row into comma-separated text.
func ParseCSV(r io.Reader) ([]string, error) {
	reader := csv.NewReader(r)
	// Relax rules for varying column counts
	reader.FieldsPerRecord = -1

	records, err := reader.ReadAll()
	if err != nil {
		return nil, err
	}

	var buf bytes.Buffer
	for _, record := range records {
		buf.WriteString(strings.Join(record, ", "))
		buf.WriteString("\n")
	}

	return []string{buf.String()}, nil
}
