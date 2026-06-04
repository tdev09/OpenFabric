package bench

import (
	"bytes"
	"context"
	"crypto/rand"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"time"
)

// StorageSuite measures storage sync throughput.
// It writes files of increasing sizes and measures how fast they propagate
// to all nodes (MB/s write + sync latency).
type StorageSuite struct {
	agentURL string
	client   *http.Client
}

// NewStorageSuite creates a storage benchmark suite.
func NewStorageSuite(agentURL string) *StorageSuite {
	return &StorageSuite{
		agentURL: agentURL,
		client:   &http.Client{Timeout: 120 * time.Second},
	}
}

// fileSizes defines the test payload sizes in bytes.
// Tests small (latency-bound), medium, and large (throughput-bound) files.
var fileSizes = []struct {
	Name  string
	Bytes int
}{
	{"1KB", 1 * 1024},
	{"64KB", 64 * 1024},
	{"1MB", 1 * 1024 * 1024},
	{"10MB", 10 * 1024 * 1024},
}

// Run uploads test files and measures upload + sync throughput.
func (s *StorageSuite) Run(ctx context.Context, nodes []string) (*SuiteResult, error) {
	result := &SuiteResult{
		Suite:     SuiteStorage,
		StartedAt: time.Now(),
		Nodes:     nodes,
		Unit:      "MB/s",
	}

	var measurements []Measurement

	for _, fs := range fileSizes {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}

		// Generate cryptographically random payload - prevents compression
		// cheating (some systems compress benchmark data, inflating results)
		payload := make([]byte, fs.Bytes)
		if _, err := rand.Read(payload); err != nil {
			continue
		}

		// Run 3 samples per file size
		for sample := 0; sample < 3; sample++ {
			filename := fmt.Sprintf(".bench-%s-%d-%d", fs.Name, sample, time.Now().UnixNano())
			throughput, err := s.measureUploadThroughput(ctx, filename, payload)
			if err != nil {
				continue
			}

			measurements = append(measurements, Measurement{
				Value: throughput,
				At:    time.Now(),
				Meta: map[string]string{
					"file_size": fs.Name,
					"bytes":     fmt.Sprintf("%d", fs.Bytes),
				},
			})

			// Clean up bench file from storage
			s.deleteFile(ctx, filename)
		}
	}

	result.Measurements = measurements
	result.Stats = ComputeStats(measurements)
	result.Samples = len(measurements)
	result.FinishedAt = time.Now()
	result.Duration = result.FinishedAt.Sub(result.StartedAt)
	return result, nil
}

// measureUploadThroughput uploads a file and returns bytes/sec throughput.
func (s *StorageSuite) measureUploadThroughput(ctx context.Context, filename string, payload []byte) (float64, error) {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	fw, err := w.CreateFormFile("file", filename)
	if err != nil {
		return 0, err
	}
	if _, err := io.Copy(fw, bytes.NewReader(payload)); err != nil {
		return 0, err
	}
	_ = w.Close()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		s.agentURL+"/api/storage/upload", &buf)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", w.FormDataContentType())

	start := time.Now()
	resp, err := s.client.Do(req)
	if err != nil {
		return 0, fmt.Errorf("upload: %w", err)
	}
	defer resp.Body.Close()
	elapsed := time.Since(start)

	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("upload HTTP %d", resp.StatusCode)
	}

	// Stored unit is MB/s
	mbps := (float64(len(payload)) / (1024 * 1024)) / elapsed.Seconds()
	return mbps, nil
}

// deleteFile removes a bench artifact from shared storage.
func (s *StorageSuite) deleteFile(ctx context.Context, filename string) {
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete,
		s.agentURL+"/api/storage/"+filename, nil)
	if err != nil {
		return
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return
	}
	_ = resp.Body.Close()
}
