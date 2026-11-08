//go:build integration

package integration_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

type positionResponse struct {
	ID          int64     `json:"id"`
	TripID      uuid.UUID `json:"trip_id"`
	RecordedAt  time.Time `json:"recorded_at"`
	Coordinates struct {
		Latitude  float64 `json:"latitude"`
		Longitude float64 `json:"longitude"`
	} `json:"coordinates"`
}

func TestTripPositionsHTTPAndMonotonicTimestamp(t *testing.T) {
	baseURL := os.Getenv("TRIPGO_BASE_URL")
	if baseURL == "" {
		t.Skip("TRIPGO_BASE_URL is not set")
	}
	client := &http.Client{Timeout: 5 * time.Second}
	createBody := []byte(strings.Replace(tripBodyTemplate, "8860b315-ec86-42eb-a17c-7c163d721ff5", uuid.NewString(), 1))
	created, err := request(client, http.MethodPost, baseURL+"/api/v1/trips", createBody)
	if err != nil {
		t.Fatal(err)
	}
	body := readAndClose(t, created)
	if created.StatusCode != http.StatusCreated {
		t.Fatalf("create trip: %d %s", created.StatusCode, body)
	}
	var trip tripResponse
	if err := json.Unmarshal(body, &trip); err != nil {
		t.Fatal(err)
	}
	path := baseURL + "/api/v1/trips/" + trip.ID + "/positions"
	defer func() {
		if trip.ID != "" {
			response, finishErr := request(client, http.MethodPost, baseURL+"/api/v1/trips/"+trip.ID+"/finish", nil)
			if finishErr == nil {
				_ = response.Body.Close()
			}
		}
	}()

	newer := time.Now().UTC().Add(-15 * time.Second).Truncate(time.Microsecond)
	older := newer.Add(-30 * time.Second)
	var first positionResponse
	for index, at := range []time.Time{newer, older} {
		payload := fmt.Sprintf(`{"coordinates":{"latitude":59.93,"longitude":30.33},"recorded_at":%q}`, at.Format(time.RFC3339Nano))
		response, postErr := request(client, http.MethodPost, path, []byte(payload))
		if postErr != nil {
			t.Fatal(postErr)
		}
		body = readAndClose(t, response)
		if response.StatusCode != http.StatusCreated {
			t.Fatalf("position %d: %d %s", index, response.StatusCode, body)
		}
		var saved positionResponse
		if err := json.Unmarshal(body, &saved); err != nil {
			t.Fatal(err)
		}
		if saved.TripID.String() != trip.ID || !saved.RecordedAt.Equal(at) || saved.ID <= 0 {
			t.Fatalf("saved position = %+v", saved)
		}
		if index == 0 {
			first = saved
		}
	}

	getTrip, err := request(client, http.MethodGet, baseURL+"/api/v1/trips/"+trip.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	body = readAndClose(t, getTrip)
	if getTrip.StatusCode != http.StatusOK {
		t.Fatalf("get trip: %d %s", getTrip.StatusCode, body)
	}
	var last struct {
		LastPositionAt *time.Time `json:"last_position_at"`
	}
	if err := json.Unmarshal(body, &last); err != nil || last.LastPositionAt == nil || !last.LastPositionAt.Equal(newer) {
		t.Fatalf("last_position_at = %v, decode error = %v", last.LastPositionAt, err)
	}

	listed, err := request(client, http.MethodGet, path, nil)
	if err != nil {
		t.Fatal(err)
	}
	body = readAndClose(t, listed)
	if listed.StatusCode != http.StatusOK {
		t.Fatalf("list positions: %d %s", listed.StatusCode, body)
	}
	var route struct {
		Positions []positionResponse `json:"positions"`
	}
	if err := json.Unmarshal(body, &route); err != nil {
		t.Fatal(err)
	}
	if len(route.Positions) != 2 || !route.Positions[0].RecordedAt.Equal(older) || route.Positions[1].ID != first.ID {
		t.Fatalf("route not sorted by recorded_at: %+v", route.Positions)
	}

	verifyPositionErrorsAndCompleted(t, client, baseURL, path, &trip, newer)
}

func verifyPositionErrorsAndCompleted(t *testing.T, client *http.Client, baseURL, path string, trip *tripResponse, newer time.Time) {
	t.Helper()
	future := fmt.Sprintf(`{"coordinates":{"latitude":59.93,"longitude":30.33},"recorded_at":%q}`, time.Now().UTC().Add(2*time.Minute).Format(time.RFC3339Nano))
	invalid, err := request(client, http.MethodPost, path, []byte(future))
	if err != nil {
		t.Fatal(err)
	}
	assertPositionProblem(t, invalid, http.StatusBadRequest, "invalid_request")
	missingPath := baseURL + "/api/v1/trips/" + uuid.NewString() + "/positions"
	missing, err := request(client, http.MethodGet, missingPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	assertPositionProblem(t, missing, http.StatusNotFound, "trip_not_found")

	finished, err := request(client, http.MethodPost, baseURL+"/api/v1/trips/"+trip.ID+"/finish", nil)
	if err != nil {
		t.Fatal(err)
	}
	body := readAndClose(t, finished)
	if finished.StatusCode != http.StatusOK {
		t.Fatalf("finish trip: %d %s", finished.StatusCode, body)
	}
	trip.ID = "" // The deferred cleanup no longer needs to finish this trip.
	newPosition, err := request(client, http.MethodPost, path, []byte(fmt.Sprintf(`{"coordinates":{"latitude":59.93,"longitude":30.33},"recorded_at":%q}`, newer.Format(time.RFC3339Nano))))
	if err != nil {
		t.Fatal(err)
	}
	assertPositionProblem(t, newPosition, http.StatusConflict, "trip_completed")
	assertStatus(t, client, http.MethodGet, path) // completed trips retain their route
}

func assertPositionProblem(t *testing.T, response *http.Response, status int, code string) {
	t.Helper()
	body := readAndClose(t, response)
	var problem problemResponse
	if err := json.Unmarshal(body, &problem); err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != status || problem.Code != code {
		t.Fatalf("problem: status %d, code %q, body %s", response.StatusCode, problem.Code, body)
	}
}
