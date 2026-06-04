package parser

import (
	"io"

	"github.com/dslipak/pdf"
)

// ParsePDF extracts plain text page-by-page from a PDF reader.
// Returns a slice of strings, where each string represents the text of one page.
func ParsePDF(r io.ReaderAt, size int64) ([]string, error) {
	reader, err := pdf.NewReader(r, size)
	if err != nil {
		return nil, err
	}

	numPages := reader.NumPage()
	pages := make([]string, 0, numPages)

	for i := 1; i <= numPages; i++ {
		page := reader.Page(i)
		if page.V.IsNull() {
			continue
		}

		// Resolve fonts map for GetPlainText
		fonts := make(map[string]*pdf.Font)
		for _, name := range page.Fonts() {
			f := page.Font(name)
			fonts[name] = &f
		}

		text, err := page.GetPlainText(fonts)
		if err != nil {
			// Skip pages that fail to parse
			continue
		}
		pages = append(pages, text)
	}

	return pages, nil
}
