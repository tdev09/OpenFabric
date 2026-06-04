package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/openfabric/openfabric/internal/agent"
	_ "github.com/openfabric/openfabric/ui"
	"go.uber.org/zap"
)

var version = "0.1.0-dev"

var osExit = os.Exit

func main() {
	if len(os.Args) > 1 && os.Args[1] == "join" {
		runJoin()
		return
	}

	if len(os.Args) > 1 && os.Args[1] == "bench" {
		runBench()
		return
	}

	if len(os.Args) > 1 && os.Args[1] == "sdn" {
		runSDN()
		return
	}

	port := flag.Int("port", 4892, "HTTP API port")
	dataDir := flag.String("data-dir", "", "Data directory (default: ~/.openfabric)")
	dev := flag.Bool("dev", false, "Development mode (verbose logging, CORS open)")
	showVersion := flag.Bool("version", false, "Print version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Printf("OpenFabric %s\n", version)
		os.Exit(0)
	}

	// First-boot detection: check if ~/.openfabric (or custom data-dir) exists.
	resolvedDataDir := *dataDir
	if resolvedDataDir == "" {
		if home, err := os.UserHomeDir(); err == nil {
			resolvedDataDir = filepath.Join(home, ".openfabric")
		}
	}

	isFirstBoot := false
	if resolvedDataDir != "" {
		if _, err := os.Stat(resolvedDataDir); os.IsNotExist(err) {
			isFirstBoot = true
		}
	}

	if isFirstBoot {
		fmt.Printf("\n=================================================================\n")
		fmt.Printf("  Welcome to OpenFabric!\n")
		fmt.Printf("  Opening dashboard at http://localhost:%d ...\n", *port)
		fmt.Printf("=================================================================\n\n")
	}

	// Always print the dashboard URL so users know where to go.
	fmt.Printf("  ✦ OpenFabric dashboard: http://localhost:%d\n\n", *port)

	// Build logger.
	var log *zap.Logger
	var err error
	if *dev {
		log, err = zap.NewDevelopment()
	} else {
		log, err = zap.NewProduction()
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to initialise logger: %v\n", err)
		os.Exit(1)
	}
	defer log.Sync() //nolint:errcheck

	cfg := agent.Config{
		APIPort: *port,
		DataDir: *dataDir,
		DevMode: *dev,
	}

	a, err := agent.New(cfg, log)
	if err != nil {
		log.Fatal("failed to create agent", zap.Error(err))
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	log.Info("OpenFabric agent starting",
		zap.String("version", version),
		zap.Int("port", *port),
	)

	// Auto-open browser after a short delay to let the server start.
	go func() {
		time.Sleep(1500 * time.Millisecond)
		openBrowser(fmt.Sprintf("http://localhost:%d", *port))
	}()

	if err := a.Run(ctx); err != nil {
		log.Fatal("agent exited with error", zap.Error(err))
	}

	log.Info("Shutting down gracefully...")

	// Give 30 seconds for in-progress tasks to finish or save state
	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancelShutdown()

	if err := a.Shutdown(shutdownCtx); err != nil {
		log.Error("Shutdown error", zap.Error(err))
	}

	// Mark any still-running tasks as interrupted
	a.MarkInterruptedTasks()

	log.Info("OpenFabric stopped cleanly")
}

func runJoin() {
	joinCmd := flag.NewFlagSet("join", flag.ExitOnError)
	port := joinCmd.Int("port", 4892, "Local agent port")

	if err := joinCmd.Parse(os.Args[2:]); err != nil {
		osExit(1)
		return
	}

	args := joinCmd.Args()
	if len(args) < 1 {
		fmt.Println("Error: connection token is required")
		fmt.Println("Usage: fabric join <connection-token>")
		osExit(1)
		return
	}

	connToken := args[0]
	if !strings.HasPrefix(connToken, "ofj_") {
		fmt.Println("Error: invalid connection token prefix (expected 'ofj_...')")
		osExit(1)
		return
	}

	// Call the local agent's join-p2p endpoint
	url := fmt.Sprintf("http://127.0.0.1:%d/api/cluster/join-p2p", *port)
	reqBody, _ := json.Marshal(map[string]string{
		"token": connToken,
	})

	client := &http.Client{Timeout: 45 * time.Second} // NAT hole-punching and relay connect might take time
	resp, err := client.Post(url, "application/json", bytes.NewReader(reqBody))
	if err != nil {
		fmt.Println("Error: OpenFabric agent is not running on this device.")
		fmt.Println("Start it first with: fabric start")
		osExit(1)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		var errResp map[string]string
		_ = json.NewDecoder(resp.Body).Decode(&errResp)
		msg := errResp["error"]
		if msg == "" {
			msg = fmt.Sprintf("HTTP %d", resp.StatusCode)
		}
		fmt.Fprintf(os.Stderr, "Error: Join failed: %s\n", msg)
		osExit(1)
		return
	}

	fmt.Println("Successfully joined the cluster over P2P!")
}

// openBrowser opens the given URL in the default browser (cross-platform).
func openBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("cmd", "/c", "start", url)
	case "darwin":
		cmd = exec.Command("open", url)
	default: // linux, freebsd, etc.
		cmd = exec.Command("xdg-open", url)
	}
	_ = cmd.Start()
}
