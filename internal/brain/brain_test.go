package brain

import (
	"os"
	"testing"
)

func TestChunkText(t *testing.T) {
	pages := []string{
		"one two three four five six",
		"seven eight nine ten",
	}

	// Max 3 words, overlap 1 word
	chunks := ChunkText(pages, 3, 1)
	if len(chunks) != 5 {
		t.Fatalf("expected 5 chunks, got %d", len(chunks))
	}

	// Verify pages and indices
	if chunks[0].Page != 1 || chunks[0].Text != "one two three" {
		t.Errorf("chunk 0 mismatch: %+v", chunks[0])
	}
	if chunks[1].Page != 1 || chunks[1].Text != "three four five" {
		t.Errorf("chunk 1 mismatch: %+v", chunks[1])
	}
	if chunks[2].Page != 1 || chunks[2].Text != "five six" {
		t.Errorf("chunk 2 mismatch: %+v", chunks[2])
	}
	// Page 2 start
	if chunks[3].Page != 2 || chunks[3].Text != "seven eight nine" {
		t.Errorf("chunk 3 mismatch: %+v", chunks[3])
	}
	if chunks[4].Page != 2 || chunks[4].Text != "nine ten" {
		t.Errorf("chunk 4 mismatch: %+v", chunks[4])
	}
}

func TestDotProduct(t *testing.T) {
	a := []float32{1.0, 0.0, 0.0}
	b := []float32{0.0, 1.0, 0.0}
	c := []float32{0.5, 0.5, 0.0}

	if dp := dotProduct(a, a); dp != 1.0 {
		t.Errorf("expected 1.0, got %f", dp)
	}
	if dp := dotProduct(a, b); dp != 0.0 {
		t.Errorf("expected 0.0, got %f", dp)
	}
	if dp := dotProduct(a, c); dp != 0.5 {
		t.Errorf("expected 0.5, got %f", dp)
	}
}

func TestVectorStoreIsLocal(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "openfabric-brain-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	vs, err := NewVectorStore(tmpDir)
	if err != nil {
		t.Fatalf("failed to create vector store: %v", err)
	}

	chunks := []Chunk{
		{Index: 0, Text: "shared doc chunk", Page: 1},
	}
	vecs := [][]float32{
		make([]float32, 768),
	}

	// Add shared chunk (isLocal = false)
	err = vs.AddChunks("shared.txt", "hash1", chunks, vecs, false)
	if err != nil {
		t.Fatalf("failed to add shared chunks: %v", err)
	}

	// Add local chunk (isLocal = true)
	chunksLocal := []Chunk{
		{Index: 0, Text: "local doc chunk", Page: 1},
	}
	err = vs.AddChunks("/abs/path/local.txt", "hash2", chunksLocal, vecs, true)
	if err != nil {
		t.Fatalf("failed to add local chunks: %v", err)
	}

	// Local search to verify
	results := vs.LocalSearch(make([]float32, 768), 10)
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}

	var foundLocal, foundShared bool
	for _, res := range results {
		if res.SourceFile == "shared.txt" {
			foundShared = true
			if res.IsLocal {
				t.Errorf("expected shared.txt to not be local")
			}
		}
		if res.SourceFile == "/abs/path/local.txt" {
			foundLocal = true
			if !res.IsLocal {
				t.Errorf("expected /abs/path/local.txt to be local")
			}
		}
	}

	if !foundShared || !foundLocal {
		t.Errorf("did not find both shared and local results: shared=%t, local=%t", foundShared, foundLocal)
	}
}

func TestSearchDeduplication(t *testing.T) {
	rawResults := []SearchResult{
		{
			Text:       "hello from node 1",
			SourceFile: "shared.txt",
			ChunkIndex: 0,
			FileHash:   "hash_abc",
			Score:      0.85,
		},
		{
			Text:       "hello from node 2 (identical file/chunk)",
			SourceFile: "shared.txt",
			ChunkIndex: 0,
			FileHash:   "hash_abc",
			Score:      0.92, // Higher score should be kept
		},
		{
			Text:       "another chunk",
			SourceFile: "shared.txt",
			ChunkIndex: 1,
			FileHash:   "hash_abc",
			Score:      0.70,
		},
		{
			Text:       "no hash chunk",
			SourceFile: "unique.txt",
			ChunkIndex: 0,
			FileHash:   "",
			Score:      0.65,
		},
	}

	deduped := deduplicateResults(rawResults)
	if len(deduped) != 3 {
		t.Fatalf("expected 3 deduped results, got %d", len(deduped))
	}

	// Verify that the duplicate chunk got merged and the higher score (0.92) was preserved
	var foundMerged bool
	for _, res := range deduped {
		if res.FileHash == "hash_abc" && res.ChunkIndex == 0 {
			foundMerged = true
			if res.Score != 0.92 {
				t.Errorf("expected score 0.92, got %f", res.Score)
			}
		}
	}
	if !foundMerged {
		t.Error("expected to find merged chunk in results")
	}
}

