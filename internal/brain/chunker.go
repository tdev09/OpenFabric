package brain

import "strings"

// Chunk represents a segment of text extracted from a file.
type Chunk struct {
	Text  string
	Index int // The sequence index of the chunk in the file
	Page  int // 1-indexed page number of the source file (0 if not applicable)
}

// ChunkText splits a slice of page texts into overlapping chunks of words.
func ChunkText(pages []string, maxWords, overlapWords int) []Chunk {
	if len(pages) == 0 {
		return nil
	}

	var chunks []Chunk
	chunkSeq := 0

	for pageIdx, pageText := range pages {
		pageNumber := pageIdx + 1
		// For single-page documents, if pageIdx is 0 and only 1 page, pageNumber will be 1.
		// Wait, if it is a single-page document (like a TXT file), we can treat it as page number 0 or 1.
		// Let's set pageNumber to 0 if len(pages) == 1 to distinguish it from a multi-page PDF page 1.
		if len(pages) == 1 {
			pageNumber = 0
		}

		words := strings.Fields(pageText)
		if len(words) == 0 {
			continue
		}

		for i := 0; i < len(words); {
			end := i + maxWords
			if end > len(words) {
				end = len(words)
			}

			chunkWords := words[i:end]
			chunkText := strings.Join(chunkWords, " ")
			chunks = append(chunks, Chunk{
				Text:  chunkText,
				Index: chunkSeq,
				Page:  pageNumber,
			})
			chunkSeq++

			if end == len(words) {
				break
			}

			step := maxWords - overlapWords
			if step <= 0 {
				step = 1
			}
			i += step
		}
	}

	return chunks
}
