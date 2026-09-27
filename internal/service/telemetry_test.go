package service

import (
	"testing"
	"time"

	"github.com/wyw14/solo-036/internal/model"
	"github.com/wyw14/solo-036/internal/telemetry"
)

func TestPressureTelemetryFlow(t *testing.T) {
	runtime := NewRuntime()
	created, err := runtime.CreateOperation("station-a")
	if err != nil {
		t.Fatalf("create operation: %v", err)
	}
	base := time.Date(2026, 9, 26, 10, 0, 0, 0, time.UTC)
	ingest := func(station string, value float64, offset int) {
		t.Helper()
		_, err := runtime.IngestPressureReading(created.ID, PressureReadingRequest{
			StationID:  station,
			Value:      value,
			ObservedAt: base.Add(time.Duration(offset) * time.Second),
		})
		if err != nil {
			t.Fatalf("ingest %s: %v", station, err)
		}
	}
	for i := 0; i < 6; i++ {
		ingest("station-a", 12.0, i*5)
	}
	for i, value := range []float64{11.6, 11.6, 11.6, 8.0, 7.9, 7.8} {
		ingest("station-b", value, i*5)
	}

	readings, err := runtime.PressureReadings(created.ID)
	if err != nil {
		t.Fatalf("readings: %v", err)
	}
	if len(readings) != 12 {
		t.Fatalf("expected 12 readings, got %d", len(readings))
	}
	for i := 1; i < len(readings); i++ {
		if readings[i].ObservedAt.Before(readings[i-1].ObservedAt) {
			t.Fatalf("readings not ordered by time at %d", i)
		}
	}

	analysis, err := runtime.AnalyzePressure(created.ID, telemetry.AnalysisConfig{})
	if err != nil {
		t.Fatalf("analyze: %v", err)
	}
	if len(analysis.AffectedSegments) != 1 || analysis.AffectedSegments[0] != "segment-a-b" {
		t.Fatalf("affected segments = %v, want [segment-a-b]", analysis.AffectedSegments)
	}
	drops := 0
	for _, anomaly := range analysis.Anomalies {
		if anomaly.Kind == model.AnomalyRealDrop {
			drops++
			if anomaly.StationID != "station-b" {
				t.Errorf("drop station = %s, want station-b", anomaly.StationID)
			}
			if anomaly.Basis == "" || len(anomaly.Evidence) == 0 {
				t.Errorf("drop must carry calculation basis and evidence")
			}
		}
	}
	if drops != 1 {
		t.Errorf("expected 1 real drop, got %d", drops)
	}
}

func TestIngestPressureReadingValidation(t *testing.T) {
	runtime := NewRuntime()
	if _, err := runtime.IngestPressureReading("missing-run", PressureReadingRequest{StationID: "station-a", Value: 1}); err == nil {
		t.Error("expected error for unknown run")
	}
	created, err := runtime.CreateOperation("station-a")
	if err != nil {
		t.Fatalf("create operation: %v", err)
	}
	if _, err := runtime.IngestPressureReading(created.ID, PressureReadingRequest{StationID: "unknown", Value: 1}); err == nil {
		t.Error("expected error for unknown station")
	}
	if _, err := runtime.IngestPressureReading(created.ID, PressureReadingRequest{StationID: "station-a", Value: -1}); err == nil {
		t.Error("expected error for negative value")
	}
	if _, err := runtime.IngestPressureReading(created.ID, PressureReadingRequest{StationID: "station-a", Value: 99}); err == nil {
		t.Error("expected error above station pressure limit")
	}
	if _, err := runtime.IngestPressureReading(created.ID, PressureReadingRequest{StationID: "station-a", Value: 12.0}); err != nil {
		t.Errorf("valid reading rejected: %v", err)
	}
	if _, err := runtime.PressureReadings("missing-run"); err == nil {
		t.Error("expected error listing readings for unknown run")
	}
	if _, err := runtime.AnalyzePressure("missing-run", telemetry.AnalysisConfig{}); err == nil {
		t.Error("expected error analyzing unknown run")
	}
}
