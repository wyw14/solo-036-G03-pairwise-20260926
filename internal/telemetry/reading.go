package telemetry

import (
	"errors"
	"sort"
	"strings"
	"sync"
	"time"
)

// Reading is one field pressure measurement taken along the pipeline and
// associated with a pigging run, a station and a moment in time.
type Reading struct {
	RunID      string    `json:"run_id"`
	StationID  string    `json:"station_id"`
	SourceID   string    `json:"source_id,omitempty"`
	Value      float64   `json:"value"`
	ObservedAt time.Time `json:"observed_at"`
	ReceivedAt time.Time `json:"received_at"`
}

// Window bounds a query or analysis in time. A zero bound is open.
type Window struct {
	From time.Time `json:"from"`
	To   time.Time `json:"to"`
}

func (w Window) Contains(at time.Time) bool {
	if !w.From.IsZero() && at.Before(w.From) {
		return false
	}
	if !w.To.IsZero() && at.After(w.To) {
		return false
	}
	return true
}

// IngestResult summarizes one batch accepted by the store.
type IngestResult struct {
	Accepted   int `json:"accepted"`
	Duplicates int `json:"duplicates"`
}

func ValidateReading(r Reading) error {
	if strings.TrimSpace(r.RunID) == "" {
		return errors.New("reading run is required")
	}
	if strings.TrimSpace(r.StationID) == "" {
		return errors.New("reading station is required")
	}
	if r.Value < 0 {
		return errors.New("reading value must be non-negative")
	}
	if r.ObservedAt.IsZero() {
		return errors.New("reading observation time is required")
	}
	return nil
}

type bucket struct {
	index    map[string]int
	readings []Reading
}

// Store keeps field pressure readings in memory, bucketed by run and station.
type Store struct {
	mu      sync.RWMutex
	buckets map[string]*bucket
}

func NewStore() *Store { return &Store{buckets: make(map[string]*bucket)} }

func storeKey(runID, stationID string) string { return runID + "/" + stationID }

func dedupKey(r Reading) string {
	return r.SourceID + "@" + r.ObservedAt.UTC().Format("20060102T150405.999999999")
}

// Add inserts or replaces readings. A reading with the same station, source
// and observation time as an existing one is treated as a retransmission and
// overwritten, so field replays are safe.
func (s *Store) Add(readings ...Reading) IngestResult {
	s.mu.Lock()
	defer s.mu.Unlock()
	var result IngestResult
	for _, reading := range readings {
		key := storeKey(reading.RunID, reading.StationID)
		b, ok := s.buckets[key]
		if !ok {
			b = &bucket{index: make(map[string]int)}
			s.buckets[key] = b
		}
		dedup := dedupKey(reading)
		if position, exists := b.index[dedup]; exists {
			b.readings[position] = reading
			result.Duplicates++
			continue
		}
		b.index[dedup] = len(b.readings)
		b.readings = append(b.readings, reading)
		result.Accepted++
	}
	return result
}

// Query returns readings for one run, optionally restricted to a station and
// a time window, ordered by observation time.
func (s *Store) Query(runID, stationID string, window Window) []Reading {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Reading, 0)
	for key, b := range s.buckets {
		if !strings.HasPrefix(key, runID+"/") {
			continue
		}
		if stationID != "" && key != storeKey(runID, stationID) {
			continue
		}
		for _, reading := range b.readings {
			if window.Contains(reading.ObservedAt) {
				out = append(out, reading)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].ObservedAt.Equal(out[j].ObservedAt) {
			if out[i].StationID == out[j].StationID {
				return out[i].SourceID < out[j].SourceID
			}
			return out[i].StationID < out[j].StationID
		}
		return out[i].ObservedAt.Before(out[j].ObservedAt)
	})
	return out
}

// Stations lists the stations that have any reading for the run.
func (s *Store) Stations(runID string) []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]string, 0)
	for key := range s.buckets {
		run, station, ok := strings.Cut(key, "/")
		if ok && run == runID {
			out = append(out, station)
		}
	}
	sort.Strings(out)
	return out
}
