//go:build integration

package integration_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

const tripBody = `{
  "user_id":"5cb72c04-7650-45c9-a79b-bcdba0631e0c",
  "driver_id":"8860b315-ec86-42eb-a17c-7c163d721ff5",
  "start_point":{"latitude":59.9398,"longitude":30.3146},
  "end_point":{"latitude":59.929,"longitude":30.3626},
  "price":1450
}`

type tripResponse struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}

type problemResponse struct {
	Code string `json:"code"`
}

func TestTripLifecycleAndConcurrency(t *testing.T) {
	baseURL := os.Getenv("TRIPGO_BASE_URL")
	if baseURL == "" {
		t.Skip("TRIPGO_BASE_URL is not set")
	}

	// A fresh driver isolates repeated -count runs from previous test data.
	body := []byte(strings.Replace(tripBody, "8860b315-ec86-42eb-a17c-7c163d721ff5", uuid.NewString(), 1))
	client := &http.Client{Timeout: 5 * time.Second}
	assertStatus(t, client, http.MethodGet, baseURL+"/health")
	assertStatus(t, client, http.MethodGet, baseURL+"/ready")

	const attempts = 20
	start := make(chan struct{})
	results := make(chan *http.Response, attempts)
	errors := make(chan error, attempts)
	var workers sync.WaitGroup
	workers.Add(attempts)
	for range attempts {
		go func() {
			defer workers.Done()
			<-start
			response, err := request(client, http.MethodPost, baseURL+"/api/v1/trips", body)
			if err != nil {
				errors <- err
				return
			}
			results <- response
		}()
	}
	close(start)
	workers.Wait()
	close(results)
	close(errors)

	for err := range errors {
		t.Errorf("create request: %v", err)
	}

	created := 0
	conflicts := 0
	tripID := ""
	for response := range results {
		body := readAndClose(t, response)
		switch response.StatusCode {
		case http.StatusCreated:
			created++
			var trip tripResponse
			if err := json.Unmarshal(body, &trip); err != nil {
				t.Fatalf("decode created trip: %v", err)
			}
			tripID = trip.ID
		case http.StatusConflict:
			conflicts++
			var problem problemResponse
			if err := json.Unmarshal(body, &problem); err != nil {
				t.Fatalf("decode conflict: %v", err)
			}
			if problem.Code != "driver_busy" {
				t.Errorf("conflict code = %q", problem.Code)
			}
		default:
			t.Errorf("create status = %d, body = %s", response.StatusCode, body)
		}
	}
	if created != 1 || conflicts != attempts-1 {
		t.Fatalf("create results: %d created, %d conflicts", created, conflicts)
	}

	assertStatus(t, client, http.MethodGet, baseURL+"/api/v1/trips/"+tripID)

	finishStatuses := make(chan int, 2)
	finishErrors := make(chan error, 2)
	workers.Add(2)
	for range 2 {
		go func() {
			defer workers.Done()
			response, err := request(client, http.MethodPost, baseURL+"/api/v1/trips/"+tripID+"/finish", nil)
			if err != nil {
				finishErrors <- err
				return
			}
			finishStatuses <- response.StatusCode
			_ = response.Body.Close()
		}()
	}
	workers.Wait()
	close(finishStatuses)
	close(finishErrors)
	for err := range finishErrors {
		t.Errorf("finish request: %v", err)
	}

	finishOK := 0
	finishConflict := 0
	for status := range finishStatuses {
		switch status {
		case http.StatusOK:
			finishOK++
		case http.StatusConflict:
			finishConflict++
		default:
			t.Errorf("finish status = %d", status)
		}
	}
	if finishOK != 1 || finishConflict != 1 {
		t.Fatalf("finish results: %d ok, %d conflicts", finishOK, finishConflict)
	}

	// A completed trip releases the driver's partial unique-index slot.
	restarted, err := request(client, http.MethodPost, baseURL+"/api/v1/trips", body)
	if err != nil {
		t.Fatal(err)
	}
	restartedBody := readAndClose(t, restarted)
	if restarted.StatusCode != http.StatusCreated {
		t.Fatalf("create after finish: status %d, body %s", restarted.StatusCode, restartedBody)
	}
	var nextTrip tripResponse
	if err := json.Unmarshal(restartedBody, &nextTrip); err != nil {
		t.Fatal(err)
	}
	// There is no DELETE endpoint; finish the second trip to avoid leaving a
	// live driver reservation in the dedicated test database.
	assertStatus(t, client, http.MethodPost, baseURL+"/api/v1/trips/"+nextTrip.ID+"/finish")
}

func assertStatus(
	t *testing.T,
	client *http.Client,
	method string,
	url string,
) {
	t.Helper()
	response, err := request(client, method, url, nil)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	responseBody := readAndClose(t, response)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("%s %s: status %d, body %s", method, url, response.StatusCode, responseBody)
	}
}

func request(
	client *http.Client,
	method string,
	url string,
	body []byte,
) (*http.Response, error) {
	// The URL is an explicit test-only endpoint supplied by the person running the test.
	request, err := http.NewRequest(method, url, bytes.NewReader(body)) //nolint:gosec
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := client.Do(request) //nolint:gosec // See the explicit test URL note above.
	if err != nil {
		return nil, fmt.Errorf("perform request: %w", err)
	}
	return response, nil
}

func readAndClose(t *testing.T, response *http.Response) []byte {
	t.Helper()
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	return body
}
