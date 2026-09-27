package api

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/wyw14/solo-036/internal/service"
	"github.com/wyw14/solo-036/internal/telemetry"
)

type pressureReadingRequest struct {
	StationID  string  `json:"station_id"`
	Value      float64 `json:"value"`
	ObservedAt string  `json:"observed_at"`
}

func (h *Handler) IngestPressureReading(w http.ResponseWriter, r *http.Request) {
	var request pressureReadingRequest
	if err := decodeBody(w, r, &request); err != nil {
		writeError(w, r, http.StatusBadRequest, err)
		return
	}
	observedAt, err := parseObservedAt(request.ObservedAt)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, err)
		return
	}
	reading, err := h.runtime.IngestPressureReading(chi.URLParam(r, "runID"), service.PressureReadingRequest{
		StationID:  request.StationID,
		Value:      request.Value,
		ObservedAt: observedAt,
	})
	if err != nil {
		writeError(w, r, http.StatusConflict, err)
		return
	}
	writeJSON(w, http.StatusCreated, reading)
}

func (h *Handler) PressureReadings(w http.ResponseWriter, r *http.Request) {
	readings, err := h.runtime.PressureReadings(chi.URLParam(r, "runID"))
	if err != nil {
		writeError(w, r, http.StatusNotFound, err)
		return
	}
	writeJSON(w, http.StatusOK, readings)
}

func (h *Handler) PressureAnalysis(w http.ResponseWriter, r *http.Request) {
	config, err := parseAnalysisConfig(r)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, err)
		return
	}
	analysis, err := h.runtime.AnalyzePressure(chi.URLParam(r, "runID"), config)
	if err != nil {
		writeError(w, r, http.StatusNotFound, err)
		return
	}
	writeJSON(w, http.StatusOK, analysis)
}

func parseAnalysisConfig(r *http.Request) (telemetry.AnalysisConfig, error) {
	var config telemetry.AnalysisConfig
	query := r.URL.Query()
	parse := func(name string, target *float64) error {
		raw := strings.TrimSpace(query.Get(name))
		if raw == "" {
			return nil
		}
		value, err := strconv.ParseFloat(raw, 64)
		if err != nil || value <= 0 {
			return fmt.Errorf("%s must be a positive number", name)
		}
		*target = value
		return nil
	}
	if err := parse("gap_factor", &config.GapFactor); err != nil {
		return config, err
	}
	if err := parse("drop_fraction", &config.DropFraction); err != nil {
		return config, err
	}
	if err := parse("min_absolute_drop", &config.MinAbsoluteDrop); err != nil {
		return config, err
	}
	return config, nil
}
