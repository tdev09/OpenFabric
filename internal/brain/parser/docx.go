package parser

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
)

// ParseDOCX extracts paragraph-level plain text from a DOCX file reader.
// Returns a slice of strings (containing a single string with the full text).
func ParseDOCX(r io.ReaderAt, size int64) ([]string, error) {
	zr, err := zip.NewReader(r, size)
	if err != nil {
		return nil, err
	}

	var docFile *zip.File
	for _, f := range zr.File {
		if f.Name == "word/document.xml" {
			docFile = f
			break
		}
	}
	if docFile == nil {
		return nil, fmt.Errorf("invalid DOCX: missing word/document.xml")
	}

	rc, err := docFile.Open()
	if err != nil {
		return nil, err
	}
	defer rc.Close()

	var buf bytes.Buffer
	dec := xml.NewDecoder(rc)
	for {
		t, err := dec.Token()
		if err != nil {
			if err == io.EOF {
				break
			}
			return nil, err
		}
		switch se := t.(type) {
		case xml.StartElement:
			if se.Name.Local == "t" {
				var text string
				if err := dec.DecodeElement(&text, &se); err != nil {
					return nil, err
				}
				buf.WriteString(text)
				buf.WriteString(" ")
			} else if se.Name.Local == "p" || se.Name.Local == "br" || se.Name.Local == "cr" {
				buf.WriteString("\n")
			}
		}
	}

	return []string{buf.String()}, nil
}
