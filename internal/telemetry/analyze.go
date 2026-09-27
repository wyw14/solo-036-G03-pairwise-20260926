package telemetry

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/wyw14/solo-036/internal/model"
)

// PathStation is one station on the localization path, in line order.
type PathStation struct {
	StationID string
	Kilometer float64
}

// PathSegment is a line segment between two consecutive path stations.
type PathSegment struct {
	ID     string
	From   string
	To     string
	FromKm float64
	ToKm   float64
}

// AnalysisConfig tunes anomaly detection. Zero values fall back to the
// defaults applied in withDefaults.
type AnalysisConfig struct {
	GapFactor       float64 // interval multiplier above which a sampling gap is declared
	DropFraction    float64 // fraction of baseline that counts as a real drop
	MinAbsoluteDrop float64 // absolute drop floor, so tiny baselines still work
	BaselineWindow  int     // number of initial readings used to self-baseline a station
}

func (c AnalysisConfig) withDefaults() AnalysisConfig {
	if c.GapFactor <= 0 {
		c.GapFactor = 2.5
	}
	if c.DropFraction <= 0 {
		c.DropFraction = 0.10
	}
	if c.MinAbsoluteDrop <= 0 {
		c.MinAbsoluteDrop = 0.2
	}
	if c.BaselineWindow <= 0 {
		c.BaselineWindow = 3
	}
	return c
}

func (c AnalysisConfig) dropThreshold(baseline float64) float64 {
	threshold := c.DropFraction * baseline
	if threshold < c.MinAbsoluteDrop {
		threshold = c.MinAbsoluteDrop
	}
	return threshold
}

// AnalysisInput carries everything the analyzer needs. The service layer
// resolves the run's route, target pressure and station positions before
// calling Analyze.
type AnalysisInput struct {
	RunID          string
	LaunchStation  string
	TargetPressure float64
	Path           []PathStation
	Segments       []PathSegment
	PathSource     string
	StationKm      map[string]float64
	Readings       map[string][]model.PressureReading
	Config         AnalysisConfig
	Now            time.Time
}

// Analyze classifies sampling gaps and real pressure drops for a run and
// localizes each real drop to the line segments that may be affected. Every
// conclusion carries its calculation basis so an operator can re-check it.
func Analyze(in AnalysisInput) model.PressureAnalysis {
	cfg := in.Config.withDefaults()
	now := in.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}

	series := make(map[string][]model.PressureReading, len(in.Readings))
	intervals := make([]float64, 0)
	total := 0
	for station, readings := range in.Readings {
		ordered := append([]model.PressureReading(nil), readings...)
		sortReadings(ordered)
		series[station] = ordered
		total += len(ordered)
		for i := 1; i < len(ordered); i++ {
			if delta := ordered[i].ObservedAt.Sub(ordered[i-1].ObservedAt).Seconds(); delta > 0 {
				intervals = append(intervals, delta)
			}
		}
	}
	medianInterval := median(intervals)
	gapThreshold := cfg.GapFactor * medianInterval

	pathIDs := make([]string, 0, len(in.Path))
	for _, station := range in.Path {
		pathIDs = append(pathIDs, station.StationID)
	}
	report := model.PressureAnalysis{
		RunID:             in.RunID,
		GeneratedAt:       now,
		TargetPressure:    in.TargetPressure,
		Path:              pathIDs,
		MedianIntervalSec: round3(medianInterval),
		GapThresholdSec:   round3(gapThreshold),
		DropThresholdFrac: cfg.DropFraction,
		Anomalies:         make([]model.PressureAnomaly, 0),
		AffectedSegments:  make([]string, 0),
		Notes:             make([]string, 0),
	}
	if in.PathSource != "" {
		report.Notes = append(report.Notes, "localization path: "+in.PathSource)
	}
	if total == 0 {
		report.Notes = append(report.Notes, "no pressure readings recorded for this run; nothing to analyze")
		return report
	}
	report.Notes = append(report.Notes, fmt.Sprintf("sampling: %d readings at %d stations; median interval %.2fs; gap threshold %.2fs (%.1fx median)", total, len(series), medianInterval, gapThreshold, cfg.GapFactor))
	if medianInterval <= 0 {
		report.Notes = append(report.Notes, "sampling interval could not be determined; gap detection disabled")
	}

	// Order stations along the line: path stations first, then stations that
	// have readings but sit off the localization path.
	type stationRef struct {
		id     string
		km     float64
		onPath bool
	}
	ordered := make([]stationRef, 0, len(in.Path)+len(series))
	onPath := make(map[string]bool, len(in.Path))
	pathIndex := make(map[string]int, len(in.Path))
	for i, station := range in.Path {
		ordered = append(ordered, stationRef{id: station.StationID, km: station.Kilometer, onPath: true})
		onPath[station.StationID] = true
		pathIndex[station.StationID] = i
	}
	extras := make([]stationRef, 0)
	for station := range series {
		if !onPath[station] {
			extras = append(extras, stationRef{id: station, km: in.StationKm[station]})
		}
	}
	sort.Slice(extras, func(i, j int) bool {
		if extras[i].km == extras[j].km {
			return extras[i].id < extras[j].id
		}
		return extras[i].km < extras[j].km
	})
	for _, extra := range extras {
		report.Notes = append(report.Notes, fmt.Sprintf("station %s has readings but is off the localization path; segment localization unavailable", extra.id))
	}
	ordered = append(ordered, extras...)

	baselines := make(map[string]float64)
	baselineBases := make(map[string]string)
	baselineOf := func(station string) (float64, string) {
		if basis, ok := baselineBases[station]; ok {
			return baselines[station], basis
		}
		var baseline float64
		var basis string
		if station == in.LaunchStation && in.TargetPressure > 0 {
			baseline = in.TargetPressure
			basis = fmt.Sprintf("baseline %.2f = run target pressure (launch station setpoint)", baseline)
		} else {
			readings := series[station]
			n := cfg.BaselineWindow
			if len(readings) < n {
				n = len(readings)
			}
			values := make([]float64, 0, n)
			for i := 0; i < n; i++ {
				values = append(values, readings[i].Value)
			}
			baseline = median(values)
			basis = fmt.Sprintf("baseline %.2f = median of first %d reading(s) at %s (%s)", baseline, n, station, formatValues(values))
		}
		baselines[station] = baseline
		baselineBases[station] = basis
		return baseline, basis
	}

	nearestNormal := func(station string, at time.Time, tolerance float64) (model.PressureReading, float64, bool) {
		nearest, found := nearestReading(series[station], at, tolerance)
		if !found {
			return model.PressureReading{}, 0, false
		}
		baseline, _ := baselineOf(station)
		if baseline-nearest.Value > cfg.dropThreshold(baseline) {
			return nearest, baseline, false
		}
		return nearest, baseline, true
	}

	segmentIDs := func(fromIdx, toIdx int) []string {
		ids := make([]string, 0, toIdx-fromIdx)
		for k := fromIdx; k < toIdx; k++ {
			for _, segment := range in.Segments {
				if segment.From == in.Path[k].StationID && segment.To == in.Path[k+1].StationID {
					ids = append(ids, segment.ID)
					break
				}
			}
		}
		return ids
	}

	// localize walks upstream from the dropped station. The affected stretch
	// starts at the nearest upstream station that was reading normally at the
	// drop onset; missing data widens the stretch and is called out.
	localize := func(pathIdx int, start time.Time) ([]string, float64, float64, string) {
		station := in.Path[pathIdx]
		if pathIdx == 0 {
			return nil, 0, 0, fmt.Sprintf("drop observed at %s, the first station on the path; origin is at or upstream of the station, no line segment implicated", station.StationID)
		}
		tolerance := 2 * medianInterval
		if tolerance < 1 {
			tolerance = 1
		}
		boundary := -1
		uncertain := -1
		var boundaryReading model.PressureReading
		var boundaryBaseline float64
		for u := pathIdx - 1; u >= 0; u-- {
			uid := in.Path[u].StationID
			nearest, baseline, normal := nearestNormal(uid, start, tolerance)
			if nearest.ID == "" {
				uncertain = u
				break
			}
			if !normal {
				continue // upstream station shows the same drop; keep walking upstream
			}
			boundary = u
			boundaryReading = nearest
			boundaryBaseline = baseline
			break
		}
		switch {
		case boundary >= 0:
			from := in.Path[boundary]
			ids := segmentIDs(boundary, pathIdx)
			return ids, from.Kilometer, station.Kilometer, fmt.Sprintf("station %s reads %.2f at %s (within %.1fs of drop start), normal against baseline %.2f (threshold %.2f); drop originates downstream, %s (km %.1f-%.1f) possibly affected",
				from.StationID, boundaryReading.Value, formatTime(boundaryReading.ObservedAt), tolerance, boundaryBaseline, cfg.dropThreshold(boundaryBaseline), describeSegments(ids), from.Kilometer, station.Kilometer)
		case uncertain >= 0:
			from := in.Path[uncertain]
			ids := segmentIDs(uncertain, pathIdx)
			return ids, from.Kilometer, station.Kilometer, fmt.Sprintf("station %s has no readings within %.1fs of the drop start; affected extent uncertain, %s (km %.1f-%.1f) possibly affected",
				from.StationID, tolerance, describeSegments(ids), from.Kilometer, station.Kilometer)
		default:
			return nil, 0, 0, fmt.Sprintf("all upstream stations show the drop; origin is at or upstream of %s, no line segment implicated", in.Path[0].StationID)
		}
	}

	anomalies := make([]model.PressureAnomaly, 0)
	affected := make(map[string]bool)
	for _, st := range ordered {
		readings := series[st.id]
		if len(readings) == 0 {
			continue
		}
		baseline, baselineBasis := baselineOf(st.id)
		threshold := cfg.dropThreshold(baseline)

		// Sampling gaps: intervals far above the median sampling interval.
		// Missing samples are reported as gaps and never as pressure drops.
		gapAfter := make(map[int]bool)
		if medianInterval > 0 {
			for i := 1; i < len(readings); i++ {
				interval := readings[i].ObservedAt.Sub(readings[i-1].ObservedAt).Seconds()
				if interval > gapThreshold {
					gapAfter[i] = true
					anomalies = append(anomalies, model.PressureAnomaly{
						Kind:      model.AnomalySamplingGap,
						StationID: st.id,
						StartAt:   readings[i-1].ObservedAt,
						EndAt:     readings[i].ObservedAt,
						Baseline:  round3(baseline),
						Basis: fmt.Sprintf("interval %.1fs between %s and %s exceeds gap threshold %.1fs (%.1fx median %.1fs); missing samples are reported as a sampling gap, not a pressure drop",
							interval, formatTime(readings[i-1].ObservedAt), formatTime(readings[i].ObservedAt), gapThreshold, cfg.GapFactor, medianInterval),
						Evidence: []model.PressureEvidence{evidenceOf(readings[i-1]), evidenceOf(readings[i])},
					})
				}
			}
		}

		// Real drops: consecutive readings measurably below baseline. These
		// are actual measured values, so they cannot be sampling artifacts.
		low := make([]bool, len(readings))
		for i, reading := range readings {
			low[i] = baseline-reading.Value > threshold
		}
		for i := 0; i < len(readings); {
			if !low[i] {
				i++
				continue
			}
			j := i
			for j+1 < len(readings) && low[j+1] {
				j++
			}
			group := readings[i : j+1]
			minReading := group[0]
			for _, reading := range group[1:] {
				if reading.Value < minReading.Value {
					minReading = reading
				}
			}
			delta := baseline - minReading.Value
			var basis strings.Builder
			fmt.Fprintf(&basis, "%s; %d consecutive low reading(s), worst %.2f at %s; drop %.2f exceeds threshold %.2f (max(%.0f%% of baseline, %.2f)); these are measured values, so this is a real pressure drop, not missing data",
				baselineBasis, len(group), minReading.Value, formatTime(minReading.ObservedAt), delta, threshold, cfg.DropFraction*100, cfg.MinAbsoluteDrop)
			if i == 0 {
				basis.WriteString("; drop already present in the first recorded reading, onset precedes ingestion")
			} else if gapAfter[i] {
				fmt.Fprintf(&basis, "; drop first observed after a sampling gap, onset uncertain between %s and %s", formatTime(readings[i-1].ObservedAt), formatTime(readings[i].ObservedAt))
			}
			for k := i + 1; k <= j; k++ {
				if gapAfter[k] {
					basis.WriteString("; sampling gap inside the drop window, extent may be under-sampled")
					break
				}
			}
			anomaly := model.PressureAnomaly{
				Kind:      model.AnomalyRealDrop,
				StationID: st.id,
				StartAt:   group[0].ObservedAt,
				EndAt:     group[len(group)-1].ObservedAt,
				Baseline:  round3(baseline),
				Observed:  round3(minReading.Value),
				Delta:     round3(delta),
				Evidence:  evidenceFor(group, minReading),
			}
			if idx, ok := pathIndex[st.id]; ok {
				segments, fromKm, toKm, locBasis := localize(idx, group[0].ObservedAt)
				anomaly.Segments = segments
				anomaly.FromKm = fromKm
				anomaly.ToKm = toKm
				basis.WriteString("; " + locBasis)
				for _, segment := range segments {
					affected[segment] = true
				}
			} else {
				basis.WriteString("; station is off the localization path, segment localization unavailable")
			}
			anomaly.Basis = basis.String()
			anomalies = append(anomalies, anomaly)
			i = j + 1
		}
	}

	sort.SliceStable(anomalies, func(i, j int) bool {
		if anomalies[i].StartAt.Equal(anomalies[j].StartAt) {
			if anomalies[i].StationID == anomalies[j].StationID {
				return anomalies[i].Kind < anomalies[j].Kind
			}
			return anomalies[i].StationID < anomalies[j].StationID
		}
		return anomalies[i].StartAt.Before(anomalies[j].StartAt)
	})
	report.Anomalies = anomalies
	for _, segment := range in.Segments {
		if affected[segment.ID] {
			report.AffectedSegments = append(report.AffectedSegments, segment.ID)
		}
	}
	if len(report.Anomalies) == 0 {
		report.Notes = append(report.Notes, "no sampling gaps or pressure drops detected")
	}
	return report
}

func median(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	sorted := append([]float64(nil), values...)
	sort.Float64s(sorted)
	mid := len(sorted) / 2
	if len(sorted)%2 == 1 {
		return sorted[mid]
	}
	return (sorted[mid-1] + sorted[mid]) / 2
}

func nearestReading(readings []model.PressureReading, at time.Time, toleranceSec float64) (model.PressureReading, bool) {
	var best model.PressureReading
	bestDelta := 0.0
	found := false
	for _, reading := range readings {
		delta := math.Abs(reading.ObservedAt.Sub(at).Seconds())
		if delta <= toleranceSec && (!found || delta < bestDelta) {
			best = reading
			bestDelta = delta
			found = true
		}
	}
	return best, found
}

func evidenceOf(reading model.PressureReading) model.PressureEvidence {
	return model.PressureEvidence{StationID: reading.StationID, Value: reading.Value, ObservedAt: reading.ObservedAt}
}

// evidenceFor cites the readings behind a drop group. Long groups are
// capped to the first two, the worst and the last two readings.
func evidenceFor(group []model.PressureReading, minReading model.PressureReading) []model.PressureEvidence {
	pick := make(map[int]bool)
	if len(group) <= 5 {
		for i := range group {
			pick[i] = true
		}
	} else {
		pick[0] = true
		pick[1] = true
		pick[len(group)-2] = true
		pick[len(group)-1] = true
		for i, reading := range group {
			if reading.ID == minReading.ID {
				pick[i] = true
			}
		}
	}
	indices := make([]int, 0, len(pick))
	for i := range pick {
		indices = append(indices, i)
	}
	sort.Ints(indices)
	out := make([]model.PressureEvidence, 0, len(indices))
	for _, i := range indices {
		out = append(out, evidenceOf(group[i]))
	}
	return out
}

func describeSegments(ids []string) string {
	if len(ids) == 0 {
		return "no recorded segment"
	}
	return "segment(s) " + strings.Join(ids, ", ")
}

func formatTime(value time.Time) string {
	return value.UTC().Format(time.RFC3339Nano)
}

func formatValues(values []float64) string {
	parts := make([]string, 0, len(values))
	for _, value := range values {
		parts = append(parts, fmt.Sprintf("%.2f", value))
	}
	return strings.Join(parts, ", ")
}

func round3(value float64) float64 {
	return math.Round(value*1000) / 1000
}
