package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/openfabric/openfabric/internal/sdn"
)

// runSDN is called when the user runs `fabric sdn`.
func runSDN() {
	if len(os.Args) < 3 {
		printSDNUsage()
		os.Exit(1)
	}

	subCmd := os.Args[2]

	sdnCmd := flag.NewFlagSet("sdn", flag.ExitOnError)
	agentURL := sdnCmd.String("agent", "http://127.0.0.1:4892", "Local agent URL")
	_ = sdnCmd.Parse(os.Args[3:])

	client := &http.Client{Timeout: 35 * time.Second}

	switch subCmd {
	case "apply":
		if sdnCmd.NArg() < 1 {
			fmt.Println("Error: YAML path is required")
			fmt.Println("Usage: fabric sdn apply <path>")
			os.Exit(1)
		}
		path := sdnCmd.Arg(0)
		data, err := os.ReadFile(path)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error reading file: %v\n", err)
			os.Exit(1)
		}

		resp, err := client.Post(*agentURL+"/api/sdn/apply", "application/yaml", bytes.NewReader(data))
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error sending request: %v\n", err)
			os.Exit(1)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			fmt.Fprintf(os.Stderr, "Error: Apply failed: %s (Status: %d)\n", string(body), resp.StatusCode)
			os.Exit(1)
		}
		fmt.Println("SDN Topology successfully applied across cluster!")

	case "status":
		resp, err := client.Get(*agentURL + "/api/sdn/status")
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error fetching status: %v\n", err)
			os.Exit(1)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			fmt.Fprintf(os.Stderr, "Error: Fetch status failed: %s\n", string(body))
			os.Exit(1)
		}

		var status map[string]interface{}
		if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
			fmt.Fprintf(os.Stderr, "Error decoding JSON: %v\n", err)
			os.Exit(1)
		}

		fmt.Printf("Fabric SDN Status:\n")
		fmt.Printf("  Coordinator: %v\n", status["is_coordinator"])
		fmt.Printf("  Active Version: %v\n", status["active_version"])
		fmt.Printf("  Active Hash: %v\n", status["active_hash"])
		fmt.Printf("  Interface: %v\n", status["interface"])
		if lastErr, ok := status["last_error"].(string); ok && lastErr != "" {
			fmt.Printf("  Last Error: %s\n", lastErr)
		}
		fmt.Printf("\nNodes Sync Status:\n")
		if nodes, ok := status["nodes"].(map[string]interface{}); ok {
			for nid, infoRaw := range nodes {
				if info, ok := infoRaw.(map[string]interface{}); ok {
					fmt.Printf("  - Node %s (%s): Online=%v\n", nid, info["name"], info["online"])
				}
			}
		}

		fmt.Printf("\nActive Rules Diagnostic Dump:\n")
		fmt.Println(status["rules_dump"])

	case "rollback":
		resp, err := client.Post(*agentURL+"/api/sdn/rollback", "application/json", nil)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error sending request: %v\n", err)
			os.Exit(1)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			fmt.Fprintf(os.Stderr, "Error: Rollback failed: %s\n", string(body))
			os.Exit(1)
		}
		fmt.Println("SDN Topology rolled back to previous version!")

	case "diff":
		if sdnCmd.NArg() < 1 {
			fmt.Println("Error: YAML path is required")
			fmt.Println("Usage: fabric sdn diff <path>")
			os.Exit(1)
		}
		path := sdnCmd.Arg(0)
		data, err := os.ReadFile(path)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error reading file: %v\n", err)
			os.Exit(1)
		}

		resp, err := client.Get(*agentURL + "/api/sdn/status")
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error fetching status: %v\n", err)
			os.Exit(1)
		}
		defer resp.Body.Close()

		var status map[string]interface{}
		_ = json.NewDecoder(resp.Body).Decode(&status)

		newTop, err := sdn.ParseTopology(data)
		if err != nil {
			fmt.Fprintf(os.Stderr, "New configuration is invalid: %v\n", err)
			os.Exit(1)
		}

		fmt.Println("SDN Topology Diff:")
		fmt.Printf("  Target config: %s\n", newTop.Name)
		if activeConf, ok := status["config"].(map[string]interface{}); ok {
			fmt.Printf("  Active config name: %s\n", activeConf["name"])
			fmt.Printf("  Active hash: %s -> New hash: %s\n", status["active_hash"], newTop.Hash())
		} else {
			fmt.Println("  No active configuration applied yet.")
		}

	default:
		printSDNUsage()
		os.Exit(1)
	}
}

func printSDNUsage() {
	fmt.Println("Usage: fabric sdn <command> [args...]")
	fmt.Println("\nCommands:")
	fmt.Println("  apply <path>    Deploy a network topology YAML file")
	fmt.Println("  status          Show SDN synchronization status and rules")
	fmt.Println("  rollback        Rollback to previous deployed topology")
	fmt.Println("  diff <path>     Show diff between file and active deployment")
}
