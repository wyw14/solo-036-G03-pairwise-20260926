package api

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/wyw14/solo-036/internal/service"
	"github.com/wyw14/solo-036/internal/telemetry"
)

type ingestPressuresRequest struct {
	Readings []readingRequest `json:"readings"`
}

type readingRequest struct {
	StationID  string  `json:"station_id"`
	SourceID   string  `json:"source_id"`
	Value      float64 `json:"value"`
	ObservedAt string  `json:"observed_at"`
}

type ingestPressuresResponse struct {
	RunID      string `json:"run_id"`
	Accepted   int    `json:"accepted"`
	Duplicates int    `json:"duplicates"`
}

// parseWindow reads optional from/to query parameters; an omitted bound is open.
func parseWindow(r *http.Request) (telemetry.Window, error) {
	var window telemetry.Window
	query := r.URL.Query()
	if value := strings.TrimSpace(query.Get("from")); value != "" {
		parsed, err := time.Parse(time.RFC3339Nano, value)
		if err != nil {
			return telemetry.Window{}, errors.New("from must use RFC3339 format")
		}
		window.From = parsed.UTC()
	}
	if value := strings.TrimSpace(query.Get("to")); value != "" {
		parsed, err := time.Parse(time.RFC3339Nano, value)
		if err != nil {
			return telemetry.Window{}, errors.New("to must use RFC3339 format")
		}
		window.To = parsed.UTC()
	}
	if !window.From.IsZero() && !window.To.IsZero() && window.To.Before(window.From) {
		return telemetry.Window{}, errors.New("to must not be before from")
	}
	return window, nil
}

// parseAnalysisParameters reads optional drop_threshold/gap_factor overrides.
func parseAnalysisParameters(r *http.Request) (telemetry.Parameters, error) {
	params := telemetry.Parameters{DropThreshold: service.DefaultDropThreshold, GapFactor: service.DefaultGapFactor}
	query := r.URL.Query()
	if value := strings.TrimSpace(query.Get("drop_threshold")); value != "" {
		parsed, err := strconv.ParseFloat(value, 64)
		if err != nil || parsed <= 0 {
			return telemetry.Parameters{}, errors.New("drop_threshold must be a positive number")
		}
		params.DropThreshold = parsed
	}
	if value := strings.TrimSpace(query.Get("gap_factor")); value != "" {
		parsed, err := strconv.ParseFloat(value, 64)
		if err != nil || parsed <= 1 {
			return telemetry.Parameters{}, errors.New("gap_factor must be a number greater than 1")
		}
		params.GapFactor = parsed
	}
	return params, nil
}

func (h *Handler) IngestPressures(w http.ResponseWriter, r *http.Request) {
	var request ingestPressuresRequest
	if err := decodeBody(w, r, &request); err != nil {
		writeError(w, r, http.StatusBadRequest, err)
		return
	}
	inputs := make([]service.ReadingInput, 0, len(request.Readings))
	for i, reading := range request.Readings {
		observedAt, err := parseObservedAt(reading.ObservedAt)
		if err != nil {
			writeError(w, r, http.StatusBadRequest, errors.New("readings."+strconv.Itoa(i)+".observed_at: "+err.Error()))
			return
		}
		inputs = append(inputs, service.ReadingInput{
			StationID:  reading.StationID,
			SourceID:   reading.SourceID,
			Value:      reading.Value,
			ObservedAt: observedAt,
		})
	}
	runID := chi.URLParam(r, "runID")
	result, err := h.runtime.IngestPressures(runID, inputs)
	if err != nil {
		writeError(w, r, http.StatusConflict, err)
		return
	}
	writeJSON(w, http.StatusCreated, ingestPressuresResponse{RunID: runID, Accepted: result.Accepted, Duplicates: result.Duplicates})
}

func (h *Handler) ListPressures(w http.ResponseWriter, r *http.Request) {
	window, err := parseWindow(r)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, err)
		return
	}
	readings, err := h.runtime.PressureReadings(chi.URLParam(r, "runID"), strings.TrimSpace(r.URL.Query().Get("station_id")), window)
	if err != nil {
		writeError(w, r, http.StatusNotFound, err)
		return
	}
	writeJSON(w, http.StatusOK, readings)
}

func (h *Handler) PressureAnalysis(w http.ResponseWriter, r *http.Request) {
	window, err := parseWindow(r)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, err)
		return
	}
	params, err := parseAnalysisParameters(r)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, err)
		return
	}
	report, err := h.runtime.AnalyzePressures(chi.URLParam(r, "runID"), window, params)
	if err != nil {
		writeError(w, r, http.StatusNotFound, err)
		return
	}
	writeJSON(w, http.StatusOK, report)
}
