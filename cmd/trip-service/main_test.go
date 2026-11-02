package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestStartupFailureHasStructuredServiceLog(t *testing.T) {
	if os.Getenv("TRIPGO_STARTUP_TEST") == "1" {
		main()
		return
	}

	cmd := exec.Command(os.Args[0], "-test.run=^TestStartupFailureHasStructuredServiceLog$") //nolint:gosec // Execute this test binary to inspect its startup log.
	cmd.Env = append(os.Environ(), "TRIPGO_STARTUP_TEST=1", "HTTP_ADDR=", "OTEL_SERVICE_NAME=trip-service")
	output, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("process unexpectedly succeeded: %s", output)
	}

	var event map[string]any
	if err := json.Unmarshal(output, &event); err != nil {
		t.Fatalf("startup log is not JSON: %q: %v", output, err)
	}
	errorMessage, ok := event["error"].(string)
	if event["service"] != "trip-service" || event["msg"] != "trip-service exited" ||
		!ok || !strings.Contains(errorMessage, "HTTP_ADDR is required") {
		t.Fatalf("unexpected startup log: %s", output)
	}
}
