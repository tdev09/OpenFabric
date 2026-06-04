package main

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/openfabric/openfabric/internal/bench"
)

// runBench is called when the user runs `fabric bench`.
// Handles the full CLI UX: flags, progress output, result formatting.
func runBench() {
	benchCmd := flag.NewFlagSet("bench", flag.ExitOnError)
	suiteFlag := benchCmd.String("suite", "all",
		"Suite(s) to run: all, inference, scheduler, storage, network, roundtrip")
	outputFlag := benchCmd.String("output", "text",
		"Output format: text, json")
	agentURL := benchCmd.String("agent", "http://127.0.0.1:4892",
		"Local agent URL")
	_ = benchCmd.Parse(os.Args[2:])

	// Validate suite flag
	suites, err := parseSuites(*suiteFlag)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		fmt.Fprintf(os.Stderr, "Valid values: all, inference, scheduler, storage, network, roundtrip\n")
		os.Exit(1)
	}

	// Check agent is reachable
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(*agentURL + "/api/status")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: OpenFabric agent is not running.\n")
		fmt.Fprintf(os.Stderr, "Start it first with: fabric start\n")
		os.Exit(1)
	}
	_ = resp.Body.Close()

	// Get cluster info from agent
	var status struct {
		NodeCount int    `json:"node_count"`
		TotalRAM  int64  `json:"total_ram"`
		ClusterID string `json:"cluster_id"`
		Nodes     []struct {
			ID string `json:"id"`
		} `json:"nodes"`
	}
	clusterResp, err := client.Get(*agentURL + "/api/nodes")
	if err == nil && clusterResp.StatusCode == http.StatusOK {
		_ = json.NewDecoder(clusterResp.Body).Decode(&status)
		_ = clusterResp.Body.Close()
	}

	nodeIDs := make([]string, len(status.Nodes))
	for i, n := range status.Nodes {
		nodeIDs[i] = n.ID
	}
	if len(nodeIDs) == 0 {
		nodeIDs = []string{"local"}
	}

	// Print header
	if *outputFlag == "text" {
		fmt.Printf("\nOpenFabric Bench - running %s suite(s) across %d node(s)\n",
			*suiteFlag, len(nodeIDs))
		fmt.Printf("This may take a few minutes...\n\n")
	}

	// Get data directory
	home, _ := os.UserHomeDir()
	dataDir := home + "/.openfabric"

	// Load private key if available to sign the report
	var privateKey ed25519.PrivateKey
	identPath := dataDir + "/identity.json"
	if identData, err := os.ReadFile(identPath); err == nil {
		var saved struct {
			PrivateKey []byte `json:"private_key"`
		}
		if json.Unmarshal(identData, &saved) == nil {
			if priv, err := crypto.UnmarshalPrivateKey(saved.PrivateKey); err == nil {
				if rawKey, err := priv.Raw(); err == nil && len(rawKey) == 64 {
					privateKey = ed25519.PrivateKey(rawKey)
				}
			}
		}
	}

	// Run benchmarks
	runner, err := bench.NewBenchRunner(bench.Config{
		AgentURL:   *agentURL,
		DataDir:    dataDir,
		ClusterID:  status.ClusterID,
		PrivateKey: privateKey,
		OnProgress: func(suite bench.SuiteID, status string) {
			if *outputFlag == "text" {
				if status == "running" {
					fmt.Printf("  %-20s running...\n", string(suite))
				} else if status == "done" {
					fmt.Printf("  %-20s done\n", string(suite))
				} else if strings.HasPrefix(status, "error") {
					fmt.Printf("  %-20s failed (%s)\n", string(suite), status)
				}
			}
		},
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()

	report, err := runner.Run(ctx, suites, nodeIDs, status.TotalRAM)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Benchmark failed: %v\n", err)
		os.Exit(1)
	}

	// Output results
	switch *outputFlag {
	case "json":
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Println(string(data))
	default:
		bench.PrintReport(os.Stdout, report)
		fmt.Printf("Results saved to %s/bench/%s.json\n\n", dataDir, report.ID)
	}
}

// parseSuites converts the --suite flag value to a []SuiteID.
func parseSuites(flag string) ([]bench.SuiteID, error) {
	if flag == "all" {
		return bench.AllSuites, nil
	}
	var suites []bench.SuiteID
	for _, part := range strings.Split(flag, ",") {
		part = strings.TrimSpace(part)
		switch bench.SuiteID(part) {
		case bench.SuiteInference,
			bench.SuiteScheduler,
			bench.SuiteStorage,
			bench.SuiteNetwork,
			bench.SuiteRoundTrip:
			suites = append(suites, bench.SuiteID(part))
		default:
			return nil, fmt.Errorf("unknown suite %q", part)
		}
	}
	if len(suites) == 0 {
		return nil, fmt.Errorf("no valid suites specified")
	}
	return suites, nil
}
