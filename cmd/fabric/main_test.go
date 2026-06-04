package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

type exitPanic struct {
	code int
}

func runJoinTest(t *testing.T, args []string) (exitCode int, exited bool, reqBody map[string]string) {
	var receivedBody map[string]string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/cluster/join-p2p" {
			var body map[string]string
			if err := json.NewDecoder(r.Body).Decode(&body); err == nil {
				receivedBody = body
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		}
	}))
	defer server.Close()

	parts := strings.Split(server.URL, ":")
	portStr := parts[len(parts)-1]

	finalArgs := []string{"fabric", "join", "--port", portStr}
	if len(args) > 2 {
		finalArgs = append(finalArgs, args[2:]...)
	}

	oldArgs := os.Args
	oldExit := osExit
	defer func() {
		os.Args = oldArgs
		osExit = oldExit
	}()

	os.Args = finalArgs

	defer func() {
		if r := recover(); r != nil {
			if ep, ok := r.(exitPanic); ok {
				exitCode = ep.code
				exited = true
			} else {
				panic(r)
			}
		}
	}()

	osExit = func(code int) {
		panic(exitPanic{code: code})
	}

	runJoin()
	return 0, false, receivedBody
}

// TestJoinRequiresToken verifies that running join without arguments exits with error.
func TestJoinRequiresToken(t *testing.T) {
	args := []string{"fabric", "join"}
	exitCode, exited, _ := runJoinTest(t, args)
	if !exited {
		t.Fatal("expected runJoin to exit")
	}
	if exitCode != 1 {
		t.Errorf("expected exit code 1, got %d", exitCode)
	}
}

// TestJoinRequiresValidTokenPrefix verifies that connection token prefix is enforced.
func TestJoinRequiresValidTokenPrefix(t *testing.T) {
	args := []string{"fabric", "join", "invalidtoken123"}
	exitCode, exited, _ := runJoinTest(t, args)
	if !exited {
		t.Fatal("expected runJoin to exit")
	}
	if exitCode != 1 {
		t.Errorf("expected exit code 1, got %d", exitCode)
	}
}

// TestJoinParsesTokenCorrectly verifies token value is captured and relayed to join-p2p endpoint.
func TestJoinParsesTokenCorrectly(t *testing.T) {
	mockToken := "ofj_eyJ0b2tlbiI6ImFkZDEyMyIsImNvb3JkaW5hdG9yX2lwIjoiMTI3LjAuMC4xIiwicGVlcl9pZCI6IlFtQ29vcmRpbmF0b3JQZWVySUQiLCJhZGRyZXNzZXMiOlsiaXB0Y3AiXX0="
	args := []string{"fabric", "join", mockToken}
	exitCode, exited, reqBody := runJoinTest(t, args)
	if exited {
		t.Fatalf("expected clean run (exit 0), but exited with code %d", exitCode)
	}
	if reqBody == nil {
		t.Fatal("expected HTTP request to be made, but no request body received")
	}
	if reqBody["token"] != mockToken {
		t.Errorf("expected token '%s', got '%s'", mockToken, reqBody["token"])
	}
}
