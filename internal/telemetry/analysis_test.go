package telemetry

import (
	"strings"
	"testing"
	"time"
)

var testBase = time.Date(2026, 9, 26, 10, 0, 0, 0, time.UTC)

func testLine() ([]LinePoint, []LineSegment) {
	line := []LinePoint{
		{StationID: "station-a", Kilometer: 0},
		{StationID: "station-b", Kilometer: 86.4},
		{StationID: "station-c", Kilometer: 173.2},
	}
	segments := []LineSegment{
		{ID: "segment-a-b", From: "station-a", To: "station-b"},
		{ID: "segment-b-c", From: "station-b", To: "station-c"},
	}
	return line, segments
}

// seriesAt builds one reading per minute at the given values.
func seriesAt(runID, station string, values ...float64) []Reading {
	out := make([]Reading, 0, len(values))
	for i, value := range values {
		out = append(out, Reading{
			RunID:      runID,
			StationID:  station,
			SourceID:   "PT",
			Value:      value,
			ObservedAt: testBase.Add(time.Duration(i) * time.Minute),
		})
	}
	return out
}

func testParams() Parameters { return Parameters{DropThreshold: 0.2, GapFactor: 2} }

func analyze(t *testing.T, series map[string][]Reading) Report {
	t.Helper()
	line, segments := testLine()
	return Analyze("run-1", "route-1", line, segments, series, Window{}, testParams(), testBase)
}

func findingKinds(report Report) []string {
	kinds := make([]string, 0, len(report.Findings))
	for _, finding := range report.Findings {
		kinds = append(kinds, finding.Kind+"/"+finding.Classification)
	}
	return kinds
}

func TestRealDropAtMiddleStationAttributesSegment(t *testing.T) {
	report := analyze(t, map[string][]Reading{
		"station-a": seriesAt("run-1", "station-a", 10, 10, 10, 10, 10, 10, 10, 10, 10, 10),
		"station-b": seriesAt("run-1", "station-b", 10, 10, 10, 10, 10, 9.5, 9.5, 9.5, 9.5, 9.5),
		"station-c": seriesAt("run-1", "station-c", 9.8, 9.8, 9.8, 9.8, 9.8, 9.8, 9.8, 9.8, 9.8, 9.8),
	})
	if len(report.Findings) != 1 {
		t.Fatalf("expected 1 finding, got %d: %v", len(report.Findings), findingKinds(report))
	}
	finding := report.Findings[0]
	if finding.Kind != KindRealDrop || finding.Classification != ClassOrigin {
		t.Fatalf("expected origin real drop, got %s/%s", finding.Kind, finding.Classification)
	}
	if finding.StationID != "station-b" || finding.SegmentID != "segment-a-b" {
		t.Fatalf("expected station-b/segment-a-b, got %s/%s", finding.StationID, finding.SegmentID)
	}
	if finding.Basis.Delta != -0.5 {
		t.Fatalf("expected delta -0.5, got %v", finding.Basis.Delta)
	}
	if finding.Basis.UpstreamStation != "station-a" || finding.Basis.UpstreamDelta == nil || *finding.Basis.UpstreamDelta != 0 {
		t.Fatalf("expected stable upstream station-a, got %s/%v", finding.Basis.UpstreamStation, finding.Basis.UpstreamDelta)
	}
	if len(report.AffectedSegments) != 1 {
		t.Fatalf("expected 1 affected segment, got %v", report.AffectedSegments)
	}
	segment := report.AffectedSegments[0]
	if segment.SegmentID != "segment-a-b" || segment.FromKilometer != 0 || segment.ToKilometer != 86.4 {
		t.Fatalf("unexpected affected segment %+v", segment)
	}
}

func TestPressureChangeAcrossSamplingGapIsNotARealDrop(t *testing.T) {
	report := analyze(t, map[string][]Reading{
		"station-a": seriesAt("run-1", "station-a", 10, 10, 10, 10, 10, 10),
		"station-b": {
			{RunID: "run-1", StationID: "station-b", SourceID: "PT", Value: 10, ObservedAt: testBase},
			{RunID: "run-1", StationID: "station-b", SourceID: "PT", Value: 10, ObservedAt: testBase.Add(1 * time.Minute)},
			{RunID: "run-1", StationID: "station-b", SourceID: "PT", Value: 10, ObservedAt: testBase.Add(2 * time.Minute)},
			{RunID: "run-1", StationID: "station-b", SourceID: "PT", Value: 9.5, ObservedAt: testBase.Add(10 * time.Minute)},
			{RunID: "run-1", StationID: "station-b", SourceID: "PT", Value: 9.4, ObservedAt: testBase.Add(11 * time.Minute)},
		},
		"station-c": seriesAt("run-1", "station-c", 9.8, 9.8, 9.8, 9.8, 9.8, 9.8),
	})
	if len(report.Findings) != 1 || report.Findings[0].Kind != KindSamplingGap {
		t.Fatalf("expected one sampling gap finding, got %v", findingKinds(report))
	}
	finding := report.Findings[0]
	if finding.StationID != "station-b" || finding.SegmentID != "" {
		t.Fatalf("sampling gap must not attribute a segment, got %s/%s", finding.StationID, finding.SegmentID)
	}
	if !strings.Contains(finding.Basis.Explanation, "sampling gap") {
		t.Fatalf("basis should explain the gap, got %q", finding.Basis.Explanation)
	}
	if finding.Basis.UpstreamDelta != nil || finding.Basis.UpstreamPreviousAt != nil || finding.Basis.UpstreamCurrentAt != nil {
		t.Fatalf("gap finding must not carry an upstream comparison, got %+v", finding.Basis)
	}
	if len(report.AffectedSegments) != 0 {
		t.Fatalf("sampling gap must not produce affected segments, got %v", report.AffectedSegments)
	}
	stats := report.Stations[1]
	if stats.StationID != "station-b" || stats.Gaps != 1 || stats.NominalIntervalSeconds != 60 {
		t.Fatalf("unexpected station-b stats %+v", stats)
	}
}

func TestUniformDropAcrossLineIsPropagated(t *testing.T) {
	report := analyze(t, map[string][]Reading{
		"station-a": seriesAt("run-1", "station-a", 10, 10, 10, 9.5, 9.5, 9.5),
		"station-b": seriesAt("run-1", "station-b", 9.8, 9.8, 9.8, 9.3, 9.3, 9.3),
		"station-c": seriesAt("run-1", "station-c", 9.6, 9.6, 9.6, 9.1, 9.1, 9.1),
	})
	if len(report.Findings) != 3 {
		t.Fatalf("expected 3 findings, got %v", findingKinds(report))
	}
	if report.Findings[0].Classification != ClassOrigin || report.Findings[0].SegmentID != "" {
		t.Fatalf("first station drop should be origin without segment, got %+v", report.Findings[0])
	}
	for _, finding := range report.Findings[1:] {
		if finding.Classification != ClassPropagated || finding.SegmentID != "" {
			t.Fatalf("downstream drops should be propagated without segment, got %+v", finding)
		}
	}
	if len(report.AffectedSegments) != 0 {
		t.Fatalf("propagated drops must not produce affected segments, got %v", report.AffectedSegments)
	}
}

func TestDeepeningDropAttributesDownstreamSegment(t *testing.T) {
	report := analyze(t, map[string][]Reading{
		"station-a": seriesAt("run-1", "station-a", 10, 10, 10, 10, 10, 10),
		"station-b": seriesAt("run-1", "station-b", 10, 10, 10, 9.7, 9.7, 9.7),
		"station-c": seriesAt("run-1", "station-c", 9.8, 9.8, 9.8, 9.0, 9.0, 9.0),
	})
	if len(report.Findings) != 2 {
		t.Fatalf("expected 2 findings, got %v", findingKinds(report))
	}
	byStation := make(map[string]Finding)
	for _, finding := range report.Findings {
		byStation[finding.StationID] = finding
	}
	if byStation["station-b"].Classification != ClassOrigin || byStation["station-b"].SegmentID != "segment-a-b" {
		t.Fatalf("station-b should be origin on segment-a-b, got %+v", byStation["station-b"])
	}
	if byStation["station-c"].Classification != ClassDeepening || byStation["station-c"].SegmentID != "segment-b-c" {
		t.Fatalf("station-c should deepen on segment-b-c, got %+v", byStation["station-c"])
	}
	if len(report.AffectedSegments) != 2 {
		t.Fatalf("expected both segments affected, got %v", report.AffectedSegments)
	}
}

func TestDropWithoutUpstreamDataIsUnlocalized(t *testing.T) {
	report := analyze(t, map[string][]Reading{
		"station-b": seriesAt("run-1", "station-b", 10, 10, 10, 9.5, 9.5, 9.5),
		"station-c": seriesAt("run-1", "station-c", 9.8, 9.8, 9.8, 9.8, 9.8, 9.8),
	})
	if len(report.Findings) != 1 {
		t.Fatalf("expected 1 finding, got %v", findingKinds(report))
	}
	finding := report.Findings[0]
	if finding.Classification != ClassUnlocalized || finding.SegmentID != "" {
		t.Fatalf("expected unlocalized finding without segment, got %+v", finding)
	}
	if len(report.AffectedSegments) != 0 {
		t.Fatalf("unlocalized finding must not produce affected segments, got %v", report.AffectedSegments)
	}
}

func TestInsufficientDataStationsAreAnnotated(t *testing.T) {
	report := analyze(t, map[string][]Reading{
		"station-a": seriesAt("run-1", "station-a", 10, 10, 10, 10),
		"station-b": seriesAt("run-1", "station-b", 10),
	})
	if len(report.Findings) != 0 {
		t.Fatalf("expected no findings, got %v", findingKinds(report))
	}
	if report.Stations[1].Note == "" || report.Stations[1].Readings != 1 {
		t.Fatalf("single-reading station should be annotated, got %+v", report.Stations[1])
	}
	if report.Stations[2].Note != "no readings in window" {
		t.Fatalf("empty station should be annotated, got %+v", report.Stations[2])
	}
}

func TestSameInstantReadingsFromDifferentSourcesAreSkipped(t *testing.T) {
	report := analyze(t, map[string][]Reading{
		"station-a": seriesAt("run-1", "station-a", 10, 10, 10),
		"station-b": {
			{RunID: "run-1", StationID: "station-b", SourceID: "PT-1", Value: 10, ObservedAt: testBase},
			{RunID: "run-1", StationID: "station-b", SourceID: "PT-2", Value: 9.0, ObservedAt: testBase},
			{RunID: "run-1", StationID: "station-b", SourceID: "PT-1", Value: 10, ObservedAt: testBase.Add(time.Minute)},
		},
	})
	if len(report.Findings) != 0 {
		t.Fatalf("zero-duration pairs must not be flagged, got %v", findingKinds(report))
	}
}

func TestWindowFiltersReadings(t *testing.T) {
	line, segments := testLine()
	series := map[string][]Reading{
		"station-a": seriesAt("run-1", "station-a", 10, 10, 10, 10, 10, 10),
		"station-b": seriesAt("run-1", "station-b", 10, 10, 10, 10, 10, 9.5),
	}
	// Window covers only the stable prefix, so the final drop is invisible.
	window := Window{From: testBase, To: testBase.Add(4 * time.Minute)}
	report := Analyze("run-1", "route-1", line, segments, series, window, testParams(), testBase)
	if len(report.Findings) != 0 {
		t.Fatalf("drop outside the window must be ignored, got %v", findingKinds(report))
	}
	if report.Stations[1].Readings != 5 {
		t.Fatalf("expected 5 readings in window, got %d", report.Stations[1].Readings)
	}
}

func TestStoreAddDeduplicatesRetransmissions(t *testing.T) {
	store := NewStore()
	readings := seriesAt("run-1", "station-b", 10, 10, 10)
	result := store.Add(readings...)
	if result.Accepted != 3 || result.Duplicates != 0 {
		t.Fatalf("unexpected ingest result %+v", result)
	}
	replay := store.Add(readings[1], readings[2])
	if replay.Accepted != 0 || replay.Duplicates != 2 {
		t.Fatalf("retransmission should deduplicate, got %+v", replay)
	}
	if got := len(store.Query("run-1", "station-b", Window{})); got != 3 {
		t.Fatalf("expected 3 stored readings, got %d", got)
	}
}

func TestStoreQueryFiltersByRunStationAndWindow(t *testing.T) {
	store := NewStore()
	store.Add(seriesAt("run-1", "station-a", 10, 10, 10)...)
	store.Add(seriesAt("run-1", "station-b", 9, 9, 9)...)
	store.Add(seriesAt("run-2", "station-b", 8, 8, 8)...)
	if got := len(store.Query("run-1", "", Window{})); got != 6 {
		t.Fatalf("expected 6 readings for run-1, got %d", got)
	}
	if got := len(store.Query("run-1", "station-b", Window{})); got != 3 {
		t.Fatalf("expected 3 readings for run-1/station-b, got %d", got)
	}
	window := Window{From: testBase.Add(time.Minute), To: testBase.Add(time.Minute)}
	if got := len(store.Query("run-1", "station-b", window)); got != 1 {
		t.Fatalf("expected 1 reading in window, got %d", got)
	}
	stations := store.Stations("run-1")
	if len(stations) != 2 || stations[0] != "station-a" || stations[1] != "station-b" {
		t.Fatalf("unexpected stations %v", stations)
	}
}

func TestValidateReading(t *testing.T) {
	valid := Reading{RunID: "run-1", StationID: "station-a", Value: 1.5, ObservedAt: testBase}
	if err := ValidateReading(valid); err != nil {
		t.Fatalf("valid reading rejected: %v", err)
	}
	cases := []Reading{
		{StationID: "station-a", Value: 1, ObservedAt: testBase},
		{RunID: "run-1", Value: 1, ObservedAt: testBase},
		{RunID: "run-1", StationID: "station-a", Value: -1, ObservedAt: testBase},
		{RunID: "run-1", StationID: "station-a", Value: 1},
	}
	for i, reading := range cases {
		if err := ValidateReading(reading); err == nil {
			t.Fatalf("case %d should be rejected", i)
		}
	}
}
