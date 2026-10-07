package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
)

type Trip struct {
	ID             string    `json:"id,omitempty"`
	IdempotencyKey string    `json:"idempotency_key,omitempty"`
	Start          time.Time `json:"start"`
	End            time.Time `json:"end"`
	Amount         int64     `json:"amount"`
	Payment        string    `json:"payment"`
	Commission     int64     `json:"commission"`
}

type Summary struct {
	Date       string `json:"date"`
	Trips      int    `json:"trips"`
	Revenue    int64  `json:"revenue"`
	Commission int64  `json:"commission"`
	Net        int64  `json:"net"`
	Cash       int64  `json:"cash"`
	Card       int64  `json:"card"`
}

type Store struct {
	mu    sync.Mutex
	path  string
	trips []Trip
}

func tripSignature(t Trip) string {
	return fmt.Sprintf("%s|%s|%d|%s|%d",
		t.Start.UTC().Format(time.RFC3339),
		t.End.UTC().Format(time.RFC3339),
		t.Amount,
		normalizeText(t.Payment),
		t.Commission,
	)
}

func dedupeTrips(trips []Trip) []Trip {
	seen := make(map[string]struct{}, len(trips))
	out := make([]Trip, 0, len(trips))
	for _, trip := range trips {
		sig := tripSignature(trip)
		if _, exists := seen[sig]; exists {
			continue
		}
		seen[sig] = struct{}{}
		out = append(out, trip)
	}
	return out
}

func NewStore(path string) (*Store, error) {
	s := &Store{path: path}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, s.saveLocked()
	}
	if err != nil {
		return nil, err
	}
	if len(strings.TrimSpace(string(b))) == 0 {
		return s, nil
	}
	if err := json.Unmarshal(b, &s.trips); err != nil {
		return nil, fmt.Errorf("read trips: %w", err)
	}
	s.trips = dedupeTrips(s.trips)
	if len(s.trips) != 0 {
		if err := s.saveLocked(); err != nil {
			return nil, err
		}
	}
	return s, nil
}

func (s *Store) saveLocked() error {
	b, err := json.MarshalIndent(s.trips, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0644); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

func nextTripID(existing []Trip) string {
	maxNum := 0
	for _, trip := range existing {
		id := strings.TrimSpace(trip.ID)
		if strings.HasPrefix(strings.ToLower(id), "t") {
			n, err := fmt.Sscanf(id, "t%d", new(int))
			if err == nil && n == 1 {
				var num int
				_, _ = fmt.Sscanf(id, "t%d", &num)
				if num > maxNum {
					maxNum = num
				}
			}
		}
	}
	return fmt.Sprintf("t%d", maxNum+1)
}

func validateTrip(t Trip) error {
	if strings.TrimSpace(t.ID) == "" {
		return errors.New("id is required")
	}
	if !t.Start.Before(t.End) {
		return errors.New("end must be after start")
	}
	if t.Amount <= 0 {
		return errors.New("amount must be greater than 0")
	}
	if t.Commission < 0 {
		return errors.New("commission must be non-negative")
	}
	if t.Commission > t.Amount {
		return errors.New("commission cannot exceed amount")
	}
	if t.Payment != "cash" && t.Payment != "card" {
		return errors.New("payment must be cash or card")
	}
	return nil
}

func normalizeText(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func (s *Store) add(t Trip) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t.IdempotencyKey = strings.TrimSpace(t.IdempotencyKey)
	if key := normalizeText(t.IdempotencyKey); key != "" {
		for _, existing := range s.trips {
			if normalizeText(existing.IdempotencyKey) == key {
				t.ID = existing.ID
				return false, nil
			}
		}
	}
	if strings.TrimSpace(t.ID) == "" {
		t.ID = nextTripID(s.trips)
	}
	if err := validateTrip(t); err != nil {
		return false, err
	}
	for _, existing := range s.trips {
		if normalizeText(existing.ID) == normalizeText(t.ID) {
			return false, nil
		}
		if tripSignature(existing) == tripSignature(t) {
			return false, nil
		}
	}
	s.trips = append(s.trips, t)
	sort.Slice(s.trips, func(i, j int) bool { return s.trips[i].Start.Before(s.trips[j].Start) })
	if err := s.saveLocked(); err != nil {
		return false, err
	}
	return true, nil
}

func (s *Store) forDate(date string) ([]Trip, error) {
	loc, err := time.LoadLocation("Asia/Almaty")
	if err != nil {
		return nil, err
	}
	day, err := time.ParseInLocation("2006-01-02", date, loc)
	if err != nil {
		return nil, errors.New("invalid date, expected YYYY-MM-DD")
	}
	next := day.AddDate(0, 0, 1)
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Trip, 0)
	for _, t := range s.trips {
		local := t.Start.In(loc)
		if !local.Before(day) && local.Before(next) {
			out = append(out, t)
		}
	}
	return out, nil
}

func makeSummary(date string, trips []Trip) Summary {
	var sum Summary
	sum.Date = date
	sum.Trips = len(trips)
	for _, t := range trips {
		sum.Revenue += t.Amount
		sum.Commission += t.Commission
		if t.Payment == "cash" {
			sum.Cash += t.Amount
		} else {
			sum.Card += t.Amount
		}
	}
	sum.Net = sum.Revenue - sum.Commission
	return sum
}

type Server struct {
	store *Store
	mux   *http.ServeMux
}

func NewServer(store *Store) *Server {
	s := &Server{store: store, mux: http.NewServeMux()}
	s.mux.HandleFunc("GET /styles.css", func(w http.ResponseWriter, r *http.Request) {
		http.ServeFile(w, r, "web/styles.css")
	})
	s.mux.HandleFunc("GET /app.js", func(w http.ResponseWriter, r *http.Request) {
		http.ServeFile(w, r, "web/app.js")
	})
	s.mux.HandleFunc("GET /api/trips", s.trips)
	s.mux.HandleFunc("GET /api/summary", s.summary)
	s.mux.HandleFunc("GET /api/next-id", s.nextID)
	s.mux.HandleFunc("POST /api/trips", s.addTrip)
	s.mux.HandleFunc("GET /", s.index)
	return s
}

func (s *Server) trips(w http.ResponseWriter, r *http.Request) {
	date := r.URL.Query().Get("date")
	if date == "" {
		http.Error(w, "date is required", http.StatusBadRequest)
		return
	}
	trips, err := s.store.forDate(date)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusOK, trips)
}

func (s *Server) summary(w http.ResponseWriter, r *http.Request) {
	date := r.URL.Query().Get("date")
	if date == "" {
		http.Error(w, "date is required", http.StatusBadRequest)
		return
	}
	trips, err := s.store.forDate(date)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusOK, makeSummary(date, trips))
}

func (s *Server) nextID(w http.ResponseWriter, r *http.Request) {
	s.store.mu.Lock()
	defer s.store.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]string{"id": nextTripID(s.store.trips)})
}

func (s *Server) addTrip(w http.ResponseWriter, r *http.Request) {
	var t Trip
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&t); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	added, err := s.store.add(t)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if !added {
		writeJSON(w, http.StatusOK, map[string]any{"created": false, "duplicate": true, "id": t.ID, "idempotency_key": t.IdempotencyKey})
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"created": true, "duplicate": false, "id": t.ID, "idempotency_key": t.IdempotencyKey})
}

func (s *Server) index(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	file, err := os.ReadFile("web/index.html")
	if err != nil {
		http.Error(w, "index.html not found", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(file)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func main() {
	path := os.Getenv("TRIPS_FILE")
	if path == "" {
		path = "data/trips.json"
	}
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	store, err := NewStore(path)
	if err != nil {
		log.Fatal(err)
	}
	addr := ":" + port
	log.Printf("driver diary listening on http://localhost%s", addr)
	log.Fatal(http.ListenAndServe(addr, NewServer(store).mux))
}
