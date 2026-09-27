package model

import "time"

// PressureReading is one field pressure sample taken along the line,
// associated to a station, an observation time and a pigging run.
type PressureReading struct {
	ID         string    `json:"id"`
	RunID      string    `json:"run_id"`
	StationID  string    `json:"station_id"`
	Value      float64   `json:"value"`
	ObservedAt time.Time `json:"observed_at"`
}

type PressureAnomalyKind string

const (
	// AnomalyRealDrop is a confirmed pressure loss: measured readings sit
	// below the station baseline while sampling around them is continuous.
	AnomalyRealDrop PressureAnomalyKind = "real_drop"
	// AnomalySamplingGap is a window with missing samples. A gap is never
	// interpreted as a pressure drop on its own.
	AnomalySamplingGap PressureAnomalyKind = "sampling_gap"
)

// PressureEvidence is a reading cited in the calculation basis of an anomaly.
type PressureEvidence struct {
	StationID  string    `json:"station_id"`
	Value      float64   `json:"value"`
	ObservedAt time.Time `json:"observed_at"`
}

// PressureAnomaly describes one detected anomaly together with the line
// segments it may affect and the calculation basis for the conclusion.
type PressureAnomaly struct {
	Kind      PressureAnomalyKind `json:"kind"`
	StationID string              `json:"station_id"`
	StartAt   time.Time           `json:"start_at"`
	EndAt     time.Time           `json:"end_at"`
	Baseline  float64             `json:"baseline"`
	Observed  float64             `json:"observed"`
	Delta     float64             `json:"delta"`
	Segments  []string            `json:"segments,omitempty"`
	FromKm    float64             `json:"from_km"`
	ToKm      float64             `json:"to_km"`
	Basis     string              `json:"basis"`
	Evidence  []PressureEvidence  `json:"evidence"`
}

// PressureAnalysis is the anomaly localization report for one run.
type PressureAnalysis struct {
	RunID             string            `json:"run_id"`
	GeneratedAt       time.Time         `json:"generated_at"`
	TargetPressure    float64           `json:"target_pressure"`
	Path              []string          `json:"path"`
	MedianIntervalSec float64           `json:"median_interval_sec"`
	GapThresholdSec   float64           `json:"gap_threshold_sec"`
	DropThresholdFrac float64           `json:"drop_threshold_frac"`
	Anomalies         []PressureAnomaly `json:"anomalies"`
	AffectedSegments  []string          `json:"affected_segments"`
	Notes             []string          `json:"notes"`
}
