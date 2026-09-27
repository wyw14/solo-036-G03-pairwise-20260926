package telemetry

import (
	"fmt"
	"sort"
	"time"
)

// Finding kinds.
const (
	KindRealDrop    = "real_drop"
	KindSamplingGap = "sampling_gap"
)

// Drop classifications describing how a real drop relates to its upstream
// neighbour on the line.
const (
	ClassOrigin      = "origin"      // drop starts here; upstream is stable
	ClassPropagated  = "propagated"  // upstream dropped as well; origin is further upstream
	ClassDeepening   = "deepening"   // drop exceeds the upstream drop; additional loss in this segment
	ClassUnlocalized = "unlocalized" // not enough upstream data to locate the origin
)

// Parameters controls how Analyze separates sampling gaps from real drops.
type Parameters struct {
	// DropThreshold is the minimum pressure decrease (MPa) treated as significant.
	DropThreshold float64 `json:"drop_threshold"`
	// GapFactor multiplies a station's nominal sampling interval; intervals
	// longer than that are sampling gaps rather than usable samples.
	GapFactor float64 `json:"gap_factor"`
}

// LinePoint is one station on the analyzed line, in pipeline order.
type LinePoint struct {
	StationID string  `json:"station_id"`
	Kilometer float64 `json:"kilometer"`
}

// LineSegment connects two adjacent line points.
type LineSegment struct {
	ID   string `json:"id"`
	From string `json:"from"`
	To   string `json:"to"`
}

// StationStats summarizes sampling coverage for one station in the window.
type StationStats struct {
	StationID              string    `json:"station_id"`
	Kilometer              float64   `json:"kilometer"`
	Readings               int       `json:"readings"`
	FirstAt                time.Time `json:"first_at,omitempty"`
	LastAt                 time.Time `json:"last_at,omitempty"`
	NominalIntervalSeconds float64   `json:"nominal_interval_seconds,omitempty"`
	Gaps                   int       `json:"gaps"`
	MinValue               float64   `json:"min_value"`
	MaxValue               float64   `json:"max_value"`
	Note                   string    `json:"note,omitempty"`
}

// Basis records every number a finding was computed from, so the conclusion
// can be reproduced by hand.
type Basis struct {
	PreviousValue           float64    `json:"previous_value"`
	PreviousAt              time.Time  `json:"previous_at"`
	CurrentValue            float64    `json:"current_value"`
	CurrentAt               time.Time  `json:"current_at"`
	Delta                   float64    `json:"delta"`
	DropThreshold           float64    `json:"drop_threshold"`
	NominalIntervalSeconds  float64    `json:"nominal_interval_seconds"`
	ObservedIntervalSeconds float64    `json:"observed_interval_seconds"`
	GapThresholdSeconds     float64    `json:"gap_threshold_seconds"`
	UpstreamStation         string     `json:"upstream_station,omitempty"`
	UpstreamDelta           *float64   `json:"upstream_delta,omitempty"`
	UpstreamPreviousAt      *time.Time `json:"upstream_previous_at,omitempty"`
	UpstreamCurrentAt       *time.Time `json:"upstream_current_at,omitempty"`
	Explanation             string     `json:"explanation"`
}

// Finding is one detected anomaly: either a real pressure drop or a sampling
// gap across which the pressure changed significantly.
type Finding struct {
	Kind           string    `json:"kind"`
	Classification string    `json:"classification,omitempty"`
	StationID      string    `json:"station_id"`
	SegmentID      string    `json:"segment_id,omitempty"`
	FromStation    string    `json:"from_station,omitempty"`
	ToStation      string    `json:"to_station,omitempty"`
	DetectedAt     time.Time `json:"detected_at"`
	Basis          Basis     `json:"basis"`
}

// SegmentImpact is one pipeline section possibly affected by the detected
// drops, with its position along the line.
type SegmentImpact struct {
	SegmentID     string  `json:"segment_id"`
	FromStation   string  `json:"from_station"`
	ToStation     string  `json:"to_station"`
	FromKilometer float64 `json:"from_kilometer"`
	ToKilometer   float64 `json:"to_kilometer"`
	FindingCount  int     `json:"finding_count"`
}

// Report is the result of localizing pressure anomalies for one run.
type Report struct {
	RunID            string          `json:"run_id"`
	RouteID          string          `json:"route_id,omitempty"`
	GeneratedAt      time.Time       `json:"generated_at"`
	Window           Window          `json:"window"`
	Parameters       Parameters      `json:"parameters"`
	Stations         []StationStats  `json:"stations"`
	Findings         []Finding       `json:"findings"`
	AffectedSegments []SegmentImpact `json:"affected_segments"`
}

// Analyze walks the line in pipeline order and classifies every significant
// pressure decrease as either a real drop (consecutive valid samples) or a
// sampling gap (the change is only visible across missing data). Real drops
// are then attributed to a line segment by comparing with the immediate
// upstream station. series must contain one entry per line-point station,
// each sorted by observation time; readings outside window are ignored.
func Analyze(runID, routeID string, line []LinePoint, segments []LineSegment, series map[string][]Reading, window Window, params Parameters, generatedAt time.Time) Report {
	report := Report{
		RunID:            runID,
		RouteID:          routeID,
		GeneratedAt:      generatedAt,
		Window:           window,
		Parameters:       params,
		Stations:         make([]StationStats, 0, len(line)),
		Findings:         make([]Finding, 0),
		AffectedSegments: make([]SegmentImpact, 0),
	}
	segmentByEndpoints := make(map[string]LineSegment, len(segments))
	for _, segment := range segments {
		segmentByEndpoints[segment.From+"/"+segment.To] = segment
	}
	filtered := make(map[string][]Reading, len(line))
	nominals := make(map[string]float64, len(line))
	gapThresholds := make(map[string]float64, len(line))
	for _, point := range line {
		readings := readingsInWindow(series[point.StationID], window)
		filtered[point.StationID] = readings
		stats := StationStats{StationID: point.StationID, Kilometer: point.Kilometer, Readings: len(readings)}
		if len(readings) == 0 {
			stats.Note = "no readings in window"
			report.Stations = append(report.Stations, stats)
			continue
		}
		stats.FirstAt = readings[0].ObservedAt
		stats.LastAt = readings[len(readings)-1].ObservedAt
		stats.MinValue, stats.MaxValue = readings[0].Value, readings[0].Value
		for _, reading := range readings {
			if reading.Value < stats.MinValue {
				stats.MinValue = reading.Value
			}
			if reading.Value > stats.MaxValue {
				stats.MaxValue = reading.Value
			}
		}
		if len(readings) < 2 {
			stats.Note = "insufficient readings to establish a sampling interval"
			report.Stations = append(report.Stations, stats)
			continue
		}
		nominal := nominalInterval(readings)
		nominals[point.StationID] = nominal
		gapThresholds[point.StationID] = nominal * params.GapFactor
		stats.NominalIntervalSeconds = round4(nominal)
		for i := 1; i < len(readings); i++ {
			interval := readings[i].ObservedAt.Sub(readings[i-1].ObservedAt).Seconds()
			if interval > nominal*params.GapFactor {
				stats.Gaps++
			}
		}
		report.Stations = append(report.Stations, stats)
	}
	for position, point := range line {
		readings := filtered[point.StationID]
		if len(readings) < 2 {
			continue
		}
		nominal := nominals[point.StationID]
		gapThreshold := gapThresholds[point.StationID]
		for i := 1; i < len(readings); i++ {
			previous, current := readings[i-1], readings[i]
			interval := current.ObservedAt.Sub(previous.ObservedAt).Seconds()
			if interval <= 0 {
				// Same-instant readings come from different transmitters;
				// a zero-duration pair says nothing about a pressure trend.
				continue
			}
			delta := current.Value - previous.Value
			basis := Basis{
				PreviousValue:           previous.Value,
				PreviousAt:              previous.ObservedAt,
				CurrentValue:            current.Value,
				CurrentAt:               current.ObservedAt,
				Delta:                   round4(delta),
				DropThreshold:           params.DropThreshold,
				NominalIntervalSeconds:  round4(nominal),
				ObservedIntervalSeconds: round4(interval),
				GapThresholdSeconds:     round4(gapThreshold),
			}
			if interval > gapThreshold {
				if delta <= -params.DropThreshold || delta >= params.DropThreshold {
					basis.Explanation = fmt.Sprintf("pressure changed %.3f MPa across a %.0fs sampling gap (nominal %.0fs, gap threshold %.0fs); missing samples make a real drop and a data gap indistinguishable", delta, interval, nominal, gapThreshold)
					report.Findings = append(report.Findings, Finding{
						Kind:       KindSamplingGap,
						StationID:  point.StationID,
						DetectedAt: current.ObservedAt,
						Basis:      basis,
					})
				}
				continue
			}
			if delta > -params.DropThreshold {
				continue
			}
			finding := Finding{Kind: KindRealDrop, StationID: point.StationID, DetectedAt: current.ObservedAt, Basis: basis}
			if position == 0 {
				finding.Classification = ClassOrigin
				finding.Basis.Explanation = fmt.Sprintf("pressure fell %.3f MPa in %.0fs at the first station on the line; no upstream station exists, so the source is at or upstream of %s", -delta, interval, point.StationID)
			} else {
				localize(&finding, line[position-1], filtered[line[position-1].StationID], previous, current, gapThreshold, nominals[line[position-1].StationID], segmentByEndpoints, params)
			}
			report.Findings = append(report.Findings, finding)
		}
	}
	sort.Slice(report.Findings, func(i, j int) bool {
		if report.Findings[i].DetectedAt.Equal(report.Findings[j].DetectedAt) {
			return report.Findings[i].StationID < report.Findings[j].StationID
		}
		return report.Findings[i].DetectedAt.Before(report.Findings[j].DetectedAt)
	})
	report.AffectedSegments = summarizeSegments(report.Findings, line)
	return report
}

// localize compares a drop at one station with the immediate upstream station
// over the same window and attributes the finding to the segment between them.
func localize(finding *Finding, upstream LinePoint, upstreamReadings []Reading, previous, current Reading, gapThreshold, upstreamNominal float64, segments map[string]LineSegment, params Parameters) {
	finding.Basis.UpstreamStation = upstream.StationID
	tolerance := gapThreshold
	if upstreamNominal > tolerance {
		tolerance = upstreamNominal
	}
	upstreamPrevious, okPrevious := nearestReading(upstreamReadings, previous.ObservedAt, tolerance)
	upstreamCurrent, okCurrent := nearestReading(upstreamReadings, current.ObservedAt, tolerance)
	if !okPrevious || !okCurrent {
		finding.Classification = ClassUnlocalized
		finding.Basis.Explanation = fmt.Sprintf("pressure fell %.3f MPa at %s, but upstream station %s has no aligned readings within %.0fs; the affected segment cannot be determined", -finding.Basis.Delta, finding.StationID, upstream.StationID, tolerance)
		return
	}
	upstreamDelta := upstreamCurrent.Value - upstreamPrevious.Value
	finding.Basis.UpstreamDelta = &upstreamDelta
	finding.Basis.UpstreamPreviousAt = &upstreamPrevious.ObservedAt
	finding.Basis.UpstreamCurrentAt = &upstreamCurrent.ObservedAt
	segment, ok := segments[upstream.StationID+"/"+finding.StationID]
	attribute := func(classification, explanation string) {
		finding.Classification = classification
		if ok {
			finding.SegmentID = segment.ID
			finding.FromStation = segment.From
			finding.ToStation = segment.To
		}
		finding.Basis.Explanation = explanation
	}
	switch {
	case upstreamDelta > -params.DropThreshold:
		attribute(ClassOrigin, fmt.Sprintf("pressure fell %.3f MPa at %s while upstream %s stayed stable (%+.3f MPa) over the same window; the loss originates in segment %s", -finding.Basis.Delta, finding.StationID, upstream.StationID, upstreamDelta, segment.ID))
	case finding.Basis.Delta <= upstreamDelta-params.DropThreshold:
		attribute(ClassDeepening, fmt.Sprintf("pressure fell %.3f MPa at %s, exceeding the upstream drop at %s (%+.3f MPa) by more than %.2f MPa; additional loss occurs in segment %s", -finding.Basis.Delta, finding.StationID, upstream.StationID, upstreamDelta, params.DropThreshold, segment.ID))
	default:
		finding.Classification = ClassPropagated
		finding.Basis.Explanation = fmt.Sprintf("pressure fell %.3f MPa at %s and upstream %s shows a similar drop (%+.3f MPa); the origin is upstream of %s", -finding.Basis.Delta, finding.StationID, upstream.StationID, upstreamDelta, finding.StationID)
	}
}

// summarizeSegments aggregates the segments attributed to origin and
// deepening findings, in pipeline order.
func summarizeSegments(findings []Finding, line []LinePoint) []SegmentImpact {
	kilometers := make(map[string]float64, len(line))
	for _, point := range line {
		kilometers[point.StationID] = point.Kilometer
	}
	impacts := make(map[string]*SegmentImpact)
	for _, finding := range findings {
		if finding.Kind != KindRealDrop || finding.SegmentID == "" {
			continue
		}
		if finding.Classification != ClassOrigin && finding.Classification != ClassDeepening {
			continue
		}
		impact, ok := impacts[finding.SegmentID]
		if !ok {
			impact = &SegmentImpact{
				SegmentID:     finding.SegmentID,
				FromStation:   finding.FromStation,
				ToStation:     finding.ToStation,
				FromKilometer: kilometers[finding.FromStation],
				ToKilometer:   kilometers[finding.ToStation],
			}
			impacts[finding.SegmentID] = impact
		}
		impact.FindingCount++
	}
	out := make([]SegmentImpact, 0, len(impacts))
	for _, impact := range impacts {
		out = append(out, *impact)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].FromKilometer == out[j].FromKilometer {
			return out[i].SegmentID < out[j].SegmentID
		}
		return out[i].FromKilometer < out[j].FromKilometer
	})
	return out
}

// readingsInWindow filters readings (already sorted by observation time) to
// the window.
func readingsInWindow(readings []Reading, window Window) []Reading {
	out := make([]Reading, 0, len(readings))
	for _, reading := range readings {
		if window.Contains(reading.ObservedAt) {
			out = append(out, reading)
		}
	}
	return out
}

// nominalInterval returns the median sampling interval in seconds, ignoring
// zero-duration pairs produced by duplicate transmitters.
func nominalInterval(readings []Reading) float64 {
	intervals := make([]float64, 0, len(readings)-1)
	for i := 1; i < len(readings); i++ {
		interval := readings[i].ObservedAt.Sub(readings[i-1].ObservedAt).Seconds()
		if interval > 0 {
			intervals = append(intervals, interval)
		}
	}
	if len(intervals) == 0 {
		return 0
	}
	sort.Float64s(intervals)
	return intervals[len(intervals)/2]
}

// nearestReading finds the reading closest to at, provided it is within
// tolerance seconds.
func nearestReading(readings []Reading, at time.Time, tolerance float64) (Reading, bool) {
	best := -1
	bestDistance := tolerance + 1
	for i, reading := range readings {
		distance := reading.ObservedAt.Sub(at).Seconds()
		if distance < 0 {
			distance = -distance
		}
		if distance <= tolerance && distance < bestDistance {
			best = i
			bestDistance = distance
		}
	}
	if best < 0 {
		return Reading{}, false
	}
	return readings[best], true
}

func round4(value float64) float64 {
	if value >= 0 {
		return float64(int(value*10000+0.5)) / 10000
	}
	return -float64(int(-value*10000+0.5)) / 10000
}
