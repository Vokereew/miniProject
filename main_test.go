package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestMakeSummary(t *testing.T) {
	date := "2026-10-01"
	trips := []Trip{
		{ID: "t1", Start: mustTime("2026-10-01T08:10:00+05:00"), End: mustTime("2026-10-01T08:32:00+05:00"), Amount: 2400, Payment: "card", Commission: 360},
		{ID: "t2", Start: mustTime("2026-10-01T09:05:00+05:00"), End: mustTime("2026-10-01T09:20:00+05:00"), Amount: 1500, Payment: "cash", Commission: 225},
		{ID: "t3", Start: mustTime("2026-10-01T11:30:00+05:00"), End: mustTime("2026-10-01T11:50:00+05:00"), Amount: 1000, Payment: "card", Commission: 100},
	}

	sum := makeSummary(date, trips)
	if sum.Date != date {
		t.Fatalf("date = %q, want %q", sum.Date, date)
	}
	if sum.Trips != 3 {
		t.Fatalf("trips = %d, want 3", sum.Trips)
	}
	if sum.Revenue != 4900 {
		t.Fatalf("revenue = %d, want 4900", sum.Revenue)
	}
	if sum.Commission != 685 {
		t.Fatalf("commission = %d, want 685", sum.Commission)
	}
	if sum.Net != 4215 {
		t.Fatalf("net = %d, want 4215", sum.Net)
	}
	if sum.Cash != 1500 {
		t.Fatalf("cash = %d, want 1500", sum.Cash)
	}
	if sum.Card != 3400 {
		t.Fatalf("card = %d, want 3400", sum.Card)
	}
}

func TestStoreRejectsDuplicateTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "trips.json")
	store, err := NewStore(path)
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}

	trip := Trip{
		ID:         "trip-dup",
		Start:      mustTime("2026-10-01T08:10:00+05:00"),
		End:        mustTime("2026-10-01T08:32:00+05:00"),
		Amount:     2400,
		Payment:    "card",
		Commission: 360,
	}

	created, err := store.add(trip)
	if err != nil {
		t.Fatalf("first add() error = %v", err)
	}
	if !created {
		t.Fatal("first add() should succeed")
	}

	created, err = store.add(trip)
	if err != nil {
		t.Fatalf("second add() error = %v", err)
	}
	if created {
		t.Fatal("second add() should be treated as duplicate")
	}
	if len(store.trips) != 1 {
		t.Fatalf("trip count = %d, want 1", len(store.trips))
	}
}

func TestNewStoreRemovesDuplicateTrips(t *testing.T) {
	path := filepath.Join(t.TempDir(), "trips.json")
	payload := `[
		{"id":"t1","idempotency_key":"dup-1","start":"2026-10-07T09:10:00Z","end":"2026-10-07T09:12:00Z","amount":1500,"payment":"cash","commission":150},
		{"id":"t2","idempotency_key":"dup-2","start":"2026-10-07T09:10:00Z","end":"2026-10-07T09:12:00Z","amount":1500,"payment":"cash","commission":150},
		{"id":"t3","idempotency_key":"uniq-1","start":"2026-10-07T10:00:00Z","end":"2026-10-07T10:20:00Z","amount":2000,"payment":"card","commission":250}
	]`
	if err := os.WriteFile(path, []byte(payload), 0644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	store, err := NewStore(path)
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}
	if len(store.trips) != 2 {
		t.Fatalf("trip count = %d, want 2", len(store.trips))
	}
	if store.trips[0].ID != "t1" {
		t.Fatalf("first kept trip = %q, want t1", store.trips[0].ID)
	}
}

func TestAddTripAPI(t *testing.T) {
	path := filepath.Join(t.TempDir(), "trips.json")
	store, err := NewStore(path)
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}
	server := NewServer(store)

	payload := map[string]any{
		"id":         "trip-1",
		"start":      "2026-10-01T08:10:00+05:00",
		"end":        "2026-10-01T08:32:00+05:00",
		"amount":     2400,
		"payment":    "card",
		"commission": 360,
	}
	body, _ := json.Marshal(payload)

	req := httptest.NewRequest(http.MethodPost, "/api/trips", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	res := httptest.NewRecorder()
	server.mux.ServeHTTP(res, req)
	if res.Code != http.StatusCreated {
		t.Fatalf("first POST status = %d, want %d; body=%s", res.Code, http.StatusCreated, res.Body.String())
	}

	req = httptest.NewRequest(http.MethodPost, "/api/trips", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	res = httptest.NewRecorder()
	server.mux.ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("duplicate POST status = %d, want %d; body=%s", res.Code, http.StatusOK, res.Body.String())
	}
	var duplicate map[string]any
	if err := json.Unmarshal(res.Body.Bytes(), &duplicate); err != nil {
		t.Fatalf("duplicate response JSON error = %v", err)
	}
	if duplicate["duplicate"] != true {
		t.Fatalf("duplicate response = %#v, want duplicate=true", duplicate)
	}

	payload["amount"] = 0
	body, _ = json.Marshal(payload)
	req = httptest.NewRequest(http.MethodPost, "/api/trips", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	res = httptest.NewRecorder()
	server.mux.ServeHTTP(res, req)
	if res.Code != http.StatusBadRequest {
		t.Fatalf("invalid POST status = %d, want %d; body=%s", res.Code, http.StatusBadRequest, res.Body.String())
	}
}

func TestTripIdempotencyKeyIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "trips.json")
	store, err := NewStore(path)
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}
	server := NewServer(store)

	payload := map[string]any{
		"idempotency_key": "req-abc-1",
		"start":           "2026-10-01T08:10:00+05:00",
		"end":             "2026-10-01T08:32:00+05:00",
		"amount":          2400,
		"payment":         "card",
		"commission":      360,
	}
	body, _ := json.Marshal(payload)

	req := httptest.NewRequest(http.MethodPost, "/api/trips", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	res := httptest.NewRecorder()
	server.mux.ServeHTTP(res, req)
	if res.Code != http.StatusCreated {
		t.Fatalf("first trace POST status = %d, want %d; body=%s", res.Code, http.StatusCreated, res.Body.String())
	}

	req = httptest.NewRequest(http.MethodPost, "/api/trips", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	res = httptest.NewRecorder()
	server.mux.ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("duplicate trace POST status = %d, want %d; body=%s", res.Code, http.StatusOK, res.Body.String())
	}
	var duplicate map[string]any
	if err := json.Unmarshal(res.Body.Bytes(), &duplicate); err != nil {
		t.Fatalf("duplicate trace response JSON error = %v", err)
	}
	if duplicate["duplicate"] != true {
		t.Fatalf("trace duplicate response = %#v, want duplicate=true", duplicate)
	}
	if len(store.trips) != 1 {
		t.Fatalf("trip count after duplicate trace request = %d, want 1", len(store.trips))
	}
}

func TestSameTripWithDifferentIdempotencyKeyIsDuplicate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "trips.json")
	store, err := NewStore(path)
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}
	server := NewServer(store)

	first := map[string]any{
		"idempotency_key": "trace-1",
		"start":           "2026-10-01T08:10:00+05:00",
		"end":             "2026-10-01T08:32:00+05:00",
		"amount":          2400,
		"payment":         "card",
		"commission":      360,
	}
	body, _ := json.Marshal(first)
	req := httptest.NewRequest(http.MethodPost, "/api/trips", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	res := httptest.NewRecorder()
	server.mux.ServeHTTP(res, req)
	if res.Code != http.StatusCreated {
		t.Fatalf("first same-trip POST status = %d, want %d; body=%s", res.Code, http.StatusCreated, res.Body.String())
	}

	second := map[string]any{
		"idempotency_key": "trace-2",
		"start":           "2026-10-01T08:10:00+05:00",
		"end":             "2026-10-01T08:32:00+05:00",
		"amount":          2400,
		"payment":         "card",
		"commission":      360,
	}
	body, _ = json.Marshal(second)
	req = httptest.NewRequest(http.MethodPost, "/api/trips", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	res = httptest.NewRecorder()
	server.mux.ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("same-trip duplicate POST status = %d, want %d; body=%s", res.Code, http.StatusOK, res.Body.String())
	}
	if len(store.trips) != 1 {
		t.Fatalf("trip count after same-trip duplicate = %d, want 1", len(store.trips))
	}
}

func TestIdempotencyKeyIsIdempotentWithFormattingDifferences(t *testing.T) {
	path := filepath.Join(t.TempDir(), "trips.json")
	store, err := NewStore(path)
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}
	server := NewServer(store)

	first := map[string]any{
		"idempotency_key": " Req-AbC-42 ",
		"start":           "2026-10-01T08:10:00+05:00",
		"end":             "2026-10-01T08:32:00+05:00",
		"amount":          2400,
		"payment":         "card",
		"commission":      360,
	}
	body, _ := json.Marshal(first)
	req := httptest.NewRequest(http.MethodPost, "/api/trips", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	res := httptest.NewRecorder()
	server.mux.ServeHTTP(res, req)
	if res.Code != http.StatusCreated {
		t.Fatalf("first formatted trace POST status = %d, want %d; body=%s", res.Code, http.StatusCreated, res.Body.String())
	}

	second := map[string]any{
		"idempotency_key": "req-abc-42",
		"id":              "t999",
		"start":           "2026-10-01T09:00:00+05:00",
		"end":             "2026-10-01T09:20:00+05:00",
		"amount":          1800,
		"payment":         "cash",
		"commission":      180,
	}
	body, _ = json.Marshal(second)
	req = httptest.NewRequest(http.MethodPost, "/api/trips", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	res = httptest.NewRecorder()
	server.mux.ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("formatted duplicate trace POST status = %d, want %d; body=%s", res.Code, http.StatusOK, res.Body.String())
	}
	if len(store.trips) != 1 {
		t.Fatalf("trip count after formatted duplicate trace request = %d, want 1", len(store.trips))
	}
	if store.trips[0].IdempotencyKey != "Req-AbC-42" {
		t.Fatalf("stored idempotency_key = %q, want %q", store.trips[0].IdempotencyKey, "Req-AbC-42")
	}
}

func TestStaticAssetsAreServed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "trips.json")
	store, err := NewStore(path)
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}
	server := NewServer(store)

	for _, tc := range []struct {
		name string
		url  string
		want string
	}{
		{name: "css", url: "/styles.css", want: "body"},
		{name: "js", url: "/app.js", want: "const money"},
	} {
		req := httptest.NewRequest(http.MethodGet, tc.url, nil)
		res := httptest.NewRecorder()
		server.mux.ServeHTTP(res, req)
		if res.Code != http.StatusOK {
			t.Fatalf("%s status = %d, want %d; body=%s", tc.name, res.Code, http.StatusOK, res.Body.String())
		}
		if !bytes.Contains(res.Body.Bytes(), []byte(tc.want)) {
			t.Fatalf("%s body = %q, want substring %q", tc.name, res.Body.String(), tc.want)
		}
	}
}

func mustTime(value string) time.Time {
	tm, err := time.Parse(time.RFC3339, value)
	if err != nil {
		panic(err)
	}
	return tm
}
