package telemetry

import (
	"errors"
	"sort"
	"sync"

	"github.com/wyw14/solo-036/internal/model"
)

// Store keeps field pressure readings grouped by run and station.
type Store struct {
	mu    sync.Mutex
	byRun map[string]map[string][]model.PressureReading
}

func NewStore() *Store {
	return &Store{byRun: make(map[string]map[string][]model.PressureReading)}
}

func (s *Store) Add(reading model.PressureReading) error {
	if reading.ID == "" || reading.RunID == "" || reading.StationID == "" {
		return errors.New("reading identity is incomplete")
	}
	if reading.Value < 0 {
		return errors.New("reading value must be non-negative")
	}
	if reading.ObservedAt.IsZero() {
		return errors.New("reading observation time is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	stations := s.byRun[reading.RunID]
	if stations == nil {
		stations = make(map[string][]model.PressureReading)
		s.byRun[reading.RunID] = stations
	}
	stations[reading.StationID] = append(stations[reading.StationID], reading)
	return nil
}

// ByStation returns the run's readings grouped by station, each series
// ordered by observation time.
func (s *Store) ByStation(runID string) map[string][]model.PressureReading {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string][]model.PressureReading)
	for station, readings := range s.byRun[runID] {
		ordered := append([]model.PressureReading(nil), readings...)
		sortReadings(ordered)
		out[station] = ordered
	}
	return out
}

// Readings returns all of the run's readings ordered by observation time.
func (s *Store) Readings(runID string) []model.PressureReading {
	byStation := s.ByStation(runID)
	out := make([]model.PressureReading, 0)
	for _, readings := range byStation {
		out = append(out, readings...)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].ObservedAt.Equal(out[j].ObservedAt) {
			if out[i].StationID == out[j].StationID {
				return out[i].ID < out[j].ID
			}
			return out[i].StationID < out[j].StationID
		}
		return out[i].ObservedAt.Before(out[j].ObservedAt)
	})
	return out
}

func (s *Store) Count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	total := 0
	for _, stations := range s.byRun {
		for _, readings := range stations {
			total += len(readings)
		}
	}
	return total
}

func sortReadings(readings []model.PressureReading) {
	sort.SliceStable(readings, func(i, j int) bool {
		if readings[i].ObservedAt.Equal(readings[j].ObservedAt) {
			return readings[i].ID < readings[j].ID
		}
		return readings[i].ObservedAt.Before(readings[j].ObservedAt)
	})
}
