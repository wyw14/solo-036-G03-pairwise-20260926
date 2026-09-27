package service

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/wyw14/solo-036/internal/model"
	"github.com/wyw14/solo-036/internal/telemetry"
)

// MaxPressureBatch bounds one reading upload.
const MaxPressureBatch = 1000

// Default analysis parameters: a 0.2 MPa decrease is significant, and an
// interval longer than twice the nominal sampling interval is a gap.
const (
	DefaultDropThreshold = 0.2
	DefaultGapFactor     = 2.0
)

// ReadingInput is one field pressure reading offered for ingestion.
type ReadingInput struct {
	StationID  string    `json:"station_id"`
	SourceID   string    `json:"source_id,omitempty"`
	Value      float64   `json:"value"`
	ObservedAt time.Time `json:"observed_at,omitempty"`
}

// IngestPressures validates a batch of field readings, stores them against
// the run and records a durable audit event.
func (r *Runtime) IngestPressures(runID string, inputs []ReadingInput) (telemetry.IngestResult, error) {
	if _, ok := r.Runs.Get(runID); !ok {
		return telemetry.IngestResult{}, fmt.Errorf("run %s not found", runID)
	}
	if len(inputs) == 0 {
		return telemetry.IngestResult{}, errors.New("readings are required")
	}
	if len(inputs) > MaxPressureBatch {
		return telemetry.IngestResult{}, fmt.Errorf("reading batch exceeds %d entries", MaxPressureBatch)
	}
	control := r.ensureControl()
	receivedAt := time.Now().UTC()
	readings := make([]telemetry.Reading, 0, len(inputs))
	for i, input := range inputs {
		station, ok := control.Stations[input.StationID]
		if !ok {
			return telemetry.IngestResult{}, fmt.Errorf("reading %d references unknown station %s", i, input.StationID)
		}
		if !station.Enabled {
			return telemetry.IngestResult{}, fmt.Errorf("reading %d references disabled station %s", i, input.StationID)
		}
		observedAt := input.ObservedAt
		if observedAt.IsZero() {
			observedAt = receivedAt
		}
		reading := telemetry.Reading{
			RunID:      runID,
			StationID:  input.StationID,
			SourceID:   strings.TrimSpace(input.SourceID),
			Value:      input.Value,
			ObservedAt: observedAt.UTC(),
			ReceivedAt: receivedAt,
		}
		if err := telemetry.ValidateReading(reading); err != nil {
			return telemetry.IngestResult{}, fmt.Errorf("reading %d: %w", i, err)
		}
		readings = append(readings, reading)
	}
	result := r.Telemetry.Add(readings...)
	stations := make(map[string]bool)
	for _, reading := range readings {
		stations[reading.StationID] = true
	}
	ordered := make([]string, 0, len(stations))
	for station := range stations {
		ordered = append(ordered, station)
	}
	sort.Strings(ordered)
	if err := r.appendEvent(model.Event{
		RunID:   runID,
		Kind:    "pressure_readings",
		Payload: fmt.Sprintf("%d readings at %s", result.Accepted, strings.Join(ordered, ",")),
		At:      receivedAt,
	}); err != nil {
		return telemetry.IngestResult{}, err
	}
	return result, nil
}

// PressureReadings returns stored readings for a run, optionally restricted
// to one station and a time window.
func (r *Runtime) PressureReadings(runID, stationID string, window telemetry.Window) ([]telemetry.Reading, error) {
	if _, ok := r.Runs.Get(runID); !ok {
		return nil, fmt.Errorf("run %s not found", runID)
	}
	return r.Telemetry.Query(runID, stationID, window), nil
}

// AnalyzePressures localizes pressure anomalies along the run's route: it
// separates sampling gaps from real drops and attributes real drops to line
// segments by comparing each station with its upstream neighbour.
func (r *Runtime) AnalyzePressures(runID string, window telemetry.Window, params telemetry.Parameters) (telemetry.Report, error) {
	if _, ok := r.Runs.Get(runID); !ok {
		return telemetry.Report{}, fmt.Errorf("run %s not found", runID)
	}
	if params.DropThreshold <= 0 {
		return telemetry.Report{}, errors.New("drop_threshold must be positive")
	}
	if params.GapFactor <= 1 {
		return telemetry.Report{}, errors.New("gap_factor must be greater than 1")
	}
	control := r.ensureControl()
	routeState, routeFound := r.routeForRun(runID)
	line, segments := r.analysisLine(control, runID, routeState, routeFound)
	series := make(map[string][]telemetry.Reading, len(line))
	for _, point := range line {
		series[point.StationID] = r.Telemetry.Query(runID, point.StationID, window)
	}
	routeID := ""
	if routeFound {
		routeID = routeState.ID
	}
	return telemetry.Analyze(runID, routeID, line, segments, series, window, params, time.Now().UTC()), nil
}

// routeForRun resolves the route a run reserved. Active runs are found on the
// route state itself; for completed runs the reservation is released, so the
// launch event in the journal provides the route id.
func (r *Runtime) routeForRun(runID string) (model.RouteState, bool) {
	control := r.ensureControl()
	r.mu.Lock()
	for _, routeState := range control.Routes {
		if routeState.RunID == runID {
			r.mu.Unlock()
			return routeState, true
		}
	}
	r.mu.Unlock()
	for _, event := range r.Journal.Events() {
		if event.RunID == runID && event.Kind == "launch" {
			r.mu.Lock()
			routeState, ok := control.Routes[event.Payload]
			r.mu.Unlock()
			if ok {
				return routeState, true
			}
		}
	}
	return model.RouteState{}, false
}

// analysisLine builds the ordered line points and segments the analysis walks.
// With a known route it follows the network path; otherwise it falls back to
// the stations that reported readings, ordered by kilometer.
func (r *Runtime) analysisLine(control *ControlState, runID string, routeState model.RouteState, routeFound bool) ([]telemetry.LinePoint, []telemetry.LineSegment) {
	if routeFound {
		from := control.Stations[routeState.FromStation]
		to := control.Stations[routeState.ToStation]
		pressure := from.PressureLimit
		if to.PressureLimit < pressure {
			pressure = to.PressureLimit
		}
		if path, err := control.Network.Path(routeState.FromStation, routeState.ToStation, pressure); err == nil && len(path.Stations) > 0 {
			return lineFromPath(control, path.Stations, path.Segments)
		}
	}
	stationIDs := r.Telemetry.Stations(runID)
	points := make([]telemetry.LinePoint, 0, len(stationIDs))
	for _, stationID := range stationIDs {
		station, ok := control.Stations[stationID]
		if !ok {
			continue
		}
		points = append(points, telemetry.LinePoint{StationID: station.ID, Kilometer: station.Kilometer})
	}
	sort.Slice(points, func(i, j int) bool {
		if points[i].Kilometer == points[j].Kilometer {
			return points[i].StationID < points[j].StationID
		}
		return points[i].Kilometer < points[j].Kilometer
	})
	segments := make([]telemetry.LineSegment, 0)
	for _, segment := range control.Network.Segments() {
		for i := 1; i < len(points); i++ {
			if segment.From == points[i-1].StationID && segment.To == points[i].StationID {
				segments = append(segments, telemetry.LineSegment{ID: segment.ID, From: segment.From, To: segment.To})
			}
		}
	}
	return points, segments
}

func lineFromPath(control *ControlState, stationIDs, segmentIDs []string) ([]telemetry.LinePoint, []telemetry.LineSegment) {
	points := make([]telemetry.LinePoint, 0, len(stationIDs))
	for _, stationID := range stationIDs {
		station, ok := control.Stations[stationID]
		if !ok {
			continue
		}
		points = append(points, telemetry.LinePoint{StationID: station.ID, Kilometer: station.Kilometer})
	}
	byID := make(map[string]bool, len(segmentIDs))
	for _, id := range segmentIDs {
		byID[id] = true
	}
	segments := make([]telemetry.LineSegment, 0, len(segmentIDs))
	for _, segment := range control.Network.Segments() {
		if byID[segment.ID] {
			segments = append(segments, telemetry.LineSegment{ID: segment.ID, From: segment.From, To: segment.To})
		}
	}
	return points, segments
}
