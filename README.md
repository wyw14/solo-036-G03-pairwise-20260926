PigFlow is a local HTTP control service for long-distance pipeline pigging operations.

## Along-line pressure telemetry

Field pressure readings can be ingested per station and run, independently of
the compressor target. Readings are associated by run, station and timestamp.

- `POST /api/operations/{runID}/pressure-readings` — ingest one reading.
  Body: `{"station_id":"station-b","value":8.1,"observed_at":"2026-09-26T10:00:15Z"}`
  (`observed_at` defaults to now; values must be within the station limit).
- `GET /api/operations/{runID}/pressure-readings` — the run's readings,
  ordered by observation time.
- `GET /api/operations/{runID}/pressure-analysis` — anomaly localization
  report. Optional query parameters: `gap_factor` (default 2.5),
  `drop_fraction` (default 0.10), `min_absolute_drop` (default 0.2).

The analyzer distinguishes two cases:

- **sampling_gap** — an interval between two received samples exceeds
  `gap_factor` × the median sampling interval. A gap means missing data and is
  never reported as a pressure drop.
- **real_drop** — measured readings fall more than
  `max(drop_fraction × baseline, min_absolute_drop)` below the station
  baseline (the run target at the launcher, the median of the first readings
  elsewhere). Real drops are measured values, not missing data.

Real drops are localized by walking upstream along the run's route (or the
kilometer-ordered station chain when no route is reserved): the first upstream
station reading normally at drop onset bounds the affected stretch, and the
segments between it and the dropped station are returned as
`affected_segments`. Missing upstream readings widen the stretch and mark the
extent uncertain. Each anomaly includes a `basis` string describing the
thresholds, readings and reasoning behind the conclusion.
