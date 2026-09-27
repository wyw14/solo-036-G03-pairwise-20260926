package telemetry

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/wyw14/solo-036/internal/model"
)

var testBase = time.Date(2026, 9, 26, 10, 0, 0, 0, time.UTC)

func testInput() AnalysisInput {
	return AnalysisInput{
		RunID:         "run-1",
		LaunchStation: "station-a",
		Path: []PathStation{
			{StationID: "station-a", Kilometer: 0},
			{StationID: "station-b", Kilometer: 86.4},
			{StationID: "station-c", Kilometer: 173.2},
		},
		Segments: []PathSegment{
			{ID: "segment-a-b", From: "station-a", To: "station-b", FromKm: 0, ToKm: 86.4},
			{ID: "segment-b-c", From: "station-b", To: "station-c", FromKm: 86.4, ToKm: 173.2},
		},
		PathSource: "route main-station-a-station-c",
		StationKm:  map[string]float64{"station-a": 0, "station-b": 86.4, "station-c": 173.2},
		Readings:   make(map[string][]model.PressureReading),
		Now:        testBase.Add(time.Hour),
	}
}

func addSeries(in *AnalysisInput, station string, offsets []int, values []float64) {
	for i, value := range values {
		in.Readings[station] = append(in.Readings[station], model.PressureReading{
			ID:         fmt.Sprintf("%s-%d", station, offsets[i]),
			RunID:      in.RunID,
			StationID:  station,
			Value:      value,
			ObservedAt: testBase.Add(time.Duration(offsets[i]) * time.Second),
		})
	}
}

func steady(station string, value float64, count int) ([]int, []float64) {
	offsets := make([]int, count)
	values := make([]float64, count)
	for i := range offsets {
		offsets[i] = i * 5
		values[i] = value
	}
	return offsets, values
}

func anomaliesOfKind(report model.PressureAnalysis, kind model.PressureAnomalyKind) []model.PressureAnomaly {
	out := make([]model.PressureAnomaly, 0)
	for _, anomaly := range report.Anomalies {
		if anomaly.Kind == kind {
			out = append(out, anomaly)
		}
	}
	return out
}

func TestAnalyzeRealDropLocalizedToSegment(t *testing.T) {
	in := testInput()
	offsets, aValues := steady("station-a", 12.0, 6)
	addSeries(&in, "station-a", offsets, aValues)
	addSeries(&in, "station-b", offsets, []float64{11.6, 11.6, 11.6, 8.0, 7.9, 7.8})

	report := Analyze(in)
	drops := anomaliesOfKind(report, model.AnomalyRealDrop)
	if len(drops) != 1 {
		t.Fatalf("expected 1 real drop, got %d: %+v", len(drops), report.Anomalies)
	}
	drop := drops[0]
	if drop.StationID != "station-b" {
		t.Errorf("drop station = %s, want station-b", drop.StationID)
	}
	if len(drop.Segments) != 1 || drop.Segments[0] != "segment-a-b" {
		t.Errorf("drop segments = %v, want [segment-a-b]", drop.Segments)
	}
	if drop.FromKm != 0 || drop.ToKm != 86.4 {
		t.Errorf("drop extent = %.1f-%.1f, want 0-86.4", drop.FromKm, drop.ToKm)
	}
	if drop.Baseline != 11.6 || drop.Observed != 7.8 {
		t.Errorf("baseline/observed = %.2f/%.2f, want 11.60/7.80", drop.Baseline, drop.Observed)
	}
	if len(drop.Evidence) == 0 {
		t.Error("drop should cite evidence readings")
	}
	if !strings.Contains(drop.Basis, "baseline 11.60") || !strings.Contains(drop.Basis, "segment-a-b") {
		t.Errorf("basis should explain the calculation, got: %s", drop.Basis)
	}
	if gaps := anomaliesOfKind(report, model.AnomalySamplingGap); len(gaps) != 0 {
		t.Errorf("expected no sampling gaps, got %d", len(gaps))
	}
	if len(report.AffectedSegments) != 1 || report.AffectedSegments[0] != "segment-a-b" {
		t.Errorf("affected segments = %v, want [segment-a-b]", report.AffectedSegments)
	}
}

func TestAnalyzeSamplingGapIsNotADrop(t *testing.T) {
	in := testInput()
	addSeries(&in, "station-b", []int{0, 5, 10, 40, 45, 50}, []float64{11.6, 11.6, 11.6, 11.5, 11.6, 11.6})

	report := Analyze(in)
	if drops := anomaliesOfKind(report, model.AnomalyRealDrop); len(drops) != 0 {
		t.Fatalf("gap must not be classified as a drop, got %+v", drops)
	}
	gaps := anomaliesOfKind(report, model.AnomalySamplingGap)
	if len(gaps) != 1 {
		t.Fatalf("expected 1 sampling gap, got %d", len(gaps))
	}
	gap := gaps[0]
	wantStart := testBase.Add(10 * time.Second)
	wantEnd := testBase.Add(40 * time.Second)
	if !gap.StartAt.Equal(wantStart) || !gap.EndAt.Equal(wantEnd) {
		t.Errorf("gap window = %s..%s, want %s..%s", gap.StartAt, gap.EndAt, wantStart, wantEnd)
	}
	if !strings.Contains(gap.Basis, "not a pressure drop") {
		t.Errorf("gap basis should state it is not a drop, got: %s", gap.Basis)
	}
	if len(report.AffectedSegments) != 0 {
		t.Errorf("gap must not implicate segments, got %v", report.AffectedSegments)
	}
}

func TestAnalyzeDropAfterGapKeepsOnsetUncertain(t *testing.T) {
	in := testInput()
	offsets, aValues := steady("station-a", 12.0, 6)
	addSeries(&in, "station-a", offsets, aValues)
	addSeries(&in, "station-b", []int{0, 5, 10, 40, 45, 50}, []float64{11.6, 11.6, 11.6, 8.0, 7.9, 7.8})

	report := Analyze(in)
	if gaps := anomaliesOfKind(report, model.AnomalySamplingGap); len(gaps) != 1 {
		t.Fatalf("expected 1 sampling gap, got %d", len(gaps))
	}
	drops := anomaliesOfKind(report, model.AnomalyRealDrop)
	if len(drops) != 1 {
		t.Fatalf("low readings after the gap are still a real drop, got %d drops", len(drops))
	}
	if !strings.Contains(drops[0].Basis, "onset uncertain") {
		t.Errorf("drop basis should flag uncertain onset, got: %s", drops[0].Basis)
	}
	if len(drops[0].Segments) != 1 || drops[0].Segments[0] != "segment-a-b" {
		t.Errorf("drop segments = %v, want [segment-a-b]", drops[0].Segments)
	}
}

func TestAnalyzeDropAtFirstStationImplicatesNoSegment(t *testing.T) {
	in := testInput()
	in.TargetPressure = 12.0
	addSeries(&in, "station-a", []int{0, 5, 10, 15, 20}, []float64{12.0, 12.0, 12.0, 10.0, 9.9})

	report := Analyze(in)
	drops := anomaliesOfKind(report, model.AnomalyRealDrop)
	if len(drops) != 1 {
		t.Fatalf("expected 1 real drop, got %d", len(drops))
	}
	drop := drops[0]
	if drop.Baseline != 12.0 {
		t.Errorf("launch station baseline = %.2f, want run target 12.00", drop.Baseline)
	}
	if len(drop.Segments) != 0 {
		t.Errorf("first-station drop must not implicate segments, got %v", drop.Segments)
	}
	if !strings.Contains(drop.Basis, "first station on the path") {
		t.Errorf("basis should explain the origin is at or upstream of the station, got: %s", drop.Basis)
	}
}

func TestAnalyzeMultiStationDropWalksUpstream(t *testing.T) {
	in := testInput()
	offsets, aValues := steady("station-a", 12.0, 6)
	addSeries(&in, "station-a", offsets, aValues)
	addSeries(&in, "station-b", offsets, []float64{11.6, 11.6, 11.6, 8.0, 8.0, 8.0})
	addSeries(&in, "station-c", offsets, []float64{11.2, 11.2, 11.2, 7.5, 7.5, 7.5})

	report := Analyze(in)
	drops := anomaliesOfKind(report, model.AnomalyRealDrop)
	if len(drops) != 2 {
		t.Fatalf("expected 2 real drops, got %d", len(drops))
	}
	byStation := make(map[string]model.PressureAnomaly)
	for _, drop := range drops {
		byStation[drop.StationID] = drop
	}
	if got := byStation["station-b"].Segments; len(got) != 1 || got[0] != "segment-a-b" {
		t.Errorf("station-b segments = %v, want [segment-a-b]", got)
	}
	// station-c also dropped, and station-b was dropping too, so the boundary
	// is station-a and both segments are implicated.
	if got := byStation["station-c"].Segments; len(got) != 2 || got[0] != "segment-a-b" || got[1] != "segment-b-c" {
		t.Errorf("station-c segments = %v, want [segment-a-b segment-b-c]", got)
	}
	if len(report.AffectedSegments) != 2 || report.AffectedSegments[0] != "segment-a-b" || report.AffectedSegments[1] != "segment-b-c" {
		t.Errorf("affected segments = %v, want [segment-a-b segment-b-c]", report.AffectedSegments)
	}
}

func TestAnalyzeDropWithMissingUpstreamDataIsUncertain(t *testing.T) {
	in := testInput()
	// station-a has no readings at all; the extent of the drop at station-b
	// cannot be bounded, so the stretch is reported as uncertain.
	addSeries(&in, "station-b", []int{0, 5, 10, 15, 20}, []float64{11.6, 11.6, 8.0, 7.9, 7.8})

	report := Analyze(in)
	drops := anomaliesOfKind(report, model.AnomalyRealDrop)
	if len(drops) != 1 {
		t.Fatalf("expected 1 real drop, got %d", len(drops))
	}
	drop := drops[0]
	if len(drop.Segments) != 1 || drop.Segments[0] != "segment-a-b" {
		t.Errorf("drop segments = %v, want [segment-a-b]", drop.Segments)
	}
	if !strings.Contains(drop.Basis, "uncertain") {
		t.Errorf("basis should flag the uncertain extent, got: %s", drop.Basis)
	}
}

func TestAnalyzeCleanRunHasNoAnomalies(t *testing.T) {
	in := testInput()
	offsets, aValues := steady("station-a", 12.0, 6)
	addSeries(&in, "station-a", offsets, aValues)
	_, bValues := steady("station-b", 11.6, 6)
	addSeries(&in, "station-b", offsets, bValues)

	report := Analyze(in)
	if len(report.Anomalies) != 0 {
		t.Fatalf("expected no anomalies, got %+v", report.Anomalies)
	}
	if len(report.AffectedSegments) != 0 {
		t.Errorf("expected no affected segments, got %v", report.AffectedSegments)
	}
}

func TestAnalyzeNoReadings(t *testing.T) {
	report := Analyze(testInput())
	if len(report.Anomalies) != 0 {
		t.Fatalf("expected no anomalies, got %+v", report.Anomalies)
	}
	joined := strings.Join(report.Notes, "\n")
	if !strings.Contains(joined, "no pressure readings") {
		t.Errorf("notes should explain nothing was analyzed, got: %s", joined)
	}
}

func TestStoreKeepsSeriesOrdered(t *testing.T) {
	store := NewStore()
	reading := func(station string, offset int) model.PressureReading {
		return model.PressureReading{
			ID:         fmt.Sprintf("%s-%d", station, offset),
			RunID:      "run-1",
			StationID:  station,
			Value:      11.0,
			ObservedAt: testBase.Add(time.Duration(offset) * time.Second),
		}
	}
	// ingest out of order
	for _, offset := range []int{10, 0, 5} {
		if err := store.Add(reading("station-b", offset)); err != nil {
			t.Fatalf("add: %v", err)
		}
	}
	if err := store.Add(reading("station-a", 7)); err != nil {
		t.Fatalf("add: %v", err)
	}
	byStation := store.ByStation("run-1")
	series := byStation["station-b"]
	if len(series) != 3 {
		t.Fatalf("expected 3 readings, got %d", len(series))
	}
	for i, want := range []int{0, 5, 10} {
		got := int(series[i].ObservedAt.Sub(testBase).Seconds())
		if got != want {
			t.Errorf("series[%d] offset = %d, want %d", i, got, want)
		}
	}
	if store.Count() != 4 {
		t.Errorf("count = %d, want 4", store.Count())
	}
	if err := store.Add(model.PressureReading{RunID: "run-1", StationID: "station-a", Value: 1}); err == nil {
		t.Error("expected identity validation error")
	}
}
