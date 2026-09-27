package service

import (
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/wyw14/solo-036/internal/model"
	"github.com/wyw14/solo-036/internal/telemetry"
)

type PressureReadingRequest struct {
	StationID  string    `json:"station_id"`
	Value      float64   `json:"value"`
	ObservedAt time.Time `json:"observed_at"`
}

// IngestPressureReading records one field pressure reading for a run. Any
// known run accepts readings, including terminal ones, so a finished job can
// still be analyzed after the fact.
func (r *Runtime) IngestPressureReading(runID string, request PressureReadingRequest) (model.PressureReading, error) {
	if _, ok := r.Runs.Get(runID); !ok {
		return model.PressureReading{}, fmt.Errorf("run %s not found", runID)
	}
	control := r.ensureControl()
	station, ok := control.Stations[request.StationID]
	if !ok || !station.Enabled {
		return model.PressureReading{}, errors.New("pressure station is unavailable")
	}
	if request.Value < 0 || request.Value > station.PressureLimit {
		return model.PressureReading{}, fmt.Errorf("reading must be within 0 and %.2f", station.PressureLimit)
	}
	if request.ObservedAt.IsZero() {
		request.ObservedAt = time.Now().UTC()
	}
	reading := model.PressureReading{
		ID:         model.NewRevision(),
		RunID:      runID,
		StationID:  request.StationID,
		Value:      request.Value,
		ObservedAt: request.ObservedAt.UTC(),
	}
	if err := r.Telemetry.Add(reading); err != nil {
		return model.PressureReading{}, err
	}
	r.mu.Lock()
	r.control.Revision = model.NewRevision()
	r.mu.Unlock()
	if err := r.appendEvent(model.Event{
		RunID:   runID,
		Kind:    "pressure_reading",
		Payload: fmt.Sprintf("%s/%.3f", request.StationID, request.Value),
		At:      request.ObservedAt,
	}); err != nil {
		return model.PressureReading{}, err
	}
	return reading, nil
}

func (r *Runtime) PressureReadings(runID string) ([]model.PressureReading, error) {
	if _, ok := r.Runs.Get(runID); !ok {
		return nil, fmt.Errorf("run %s not found", runID)
	}
	return r.Telemetry.Readings(runID), nil
}

// AnalyzePressure runs anomaly localization over the run's pressure
// readings. The localization path follows the route reserved for the run;
// without a route it falls back to the kilometer-ordered station chain.
func (r *Runtime) AnalyzePressure(runID string, config telemetry.AnalysisConfig) (model.PressureAnalysis, error) {
	current, ok := r.Runs.Get(runID)
	if !ok {
		return model.PressureAnalysis{}, fmt.Errorf("run %s not found", runID)
	}
	control := r.ensureControl()
	input := telemetry.AnalysisInput{
		RunID:     runID,
		Readings:  r.Telemetry.ByStation(runID),
		Config:    config,
		StationKm: make(map[string]float64, len(control.Stations)),
	}
	for id, station := range control.Stations {
		input.StationKm[id] = station.Kilometer
	}
	input.LaunchStation = current.Station
	if pressureState, ok := control.Pressures[current.Station]; ok && pressureState.RunID == runID {
		input.TargetPressure = pressureState.Target
	}
	var routeState model.RouteState
	routeFound := false
	for _, candidate := range control.Routes {
		if candidate.RunID == runID {
			routeState = candidate
			routeFound = true
			break
		}
	}
	segments := control.Network.Segments()
	if routeFound {
		pressureForPath := input.TargetPressure
		if pressureForPath <= 0 {
			pressureForPath = minimumLimit(control.Stations[routeState.FromStation].PressureLimit, control.Stations[routeState.ToStation].PressureLimit)
		}
		if path, err := control.Network.Path(routeState.FromStation, routeState.ToStation, pressureForPath); err == nil {
			for _, stationID := range path.Stations {
				input.Path = append(input.Path, telemetry.PathStation{StationID: stationID, Kilometer: input.StationKm[stationID]})
			}
			for _, segmentID := range path.Segments {
				for _, segment := range segments {
					if segment.ID == segmentID {
						input.Segments = append(input.Segments, telemetry.PathSegment{ID: segment.ID, From: segment.From, To: segment.To, FromKm: input.StationKm[segment.From], ToKm: input.StationKm[segment.To]})
						break
					}
				}
			}
			input.PathSource = "route " + routeState.ID
		}
	}
	if len(input.Path) == 0 {
		stations := make([]model.Station, 0, len(control.Stations))
		for _, station := range control.Stations {
			stations = append(stations, station)
		}
		sort.Slice(stations, func(i, j int) bool {
			if stations[i].Kilometer == stations[j].Kilometer {
				return stations[i].ID < stations[j].ID
			}
			return stations[i].Kilometer < stations[j].Kilometer
		})
		for _, station := range stations {
			input.Path = append(input.Path, telemetry.PathStation{StationID: station.ID, Kilometer: station.Kilometer})
		}
		for i := 0; i+1 < len(stations); i++ {
			for _, segment := range segments {
				if segment.From == stations[i].ID && segment.To == stations[i+1].ID {
					input.Segments = append(input.Segments, telemetry.PathSegment{ID: segment.ID, From: segment.From, To: segment.To, FromKm: stations[i].Kilometer, ToKm: stations[i+1].Kilometer})
					break
				}
			}
		}
		input.PathSource = "kilometer-ordered station chain (no route reserved for run)"
	}
	return telemetry.Analyze(input), nil
}

func minimumLimit(left, right float64) float64 {
	if left < right {
		return left
	}
	return right
}
