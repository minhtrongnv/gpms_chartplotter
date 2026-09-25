package server

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/beetlebugorg/chartplotter/internal/engine/tilesource"
)

// handleDebugQuery exposes one composed tile57 pick for diagnostics only.
// ComposeSource.Query also emits its ownership explanation to the server log,
// so one request shows both feature attrs and the serving owner/tier story.
func (s *Server) handleDebugQuery(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		apiErr(w, http.StatusMethodNotAllowed, "GET only")
		return
	}

	q := r.URL.Query()
	set := q.Get("set")
	if set == "" {
		apiErr(w, http.StatusBadRequest, "set is required")
		return
	}
	lon, err := strconv.ParseFloat(q.Get("lon"), 64)
	if err != nil || lon < -180 || lon > 180 {
		apiErr(w, http.StatusBadRequest, "invalid lon")
		return
	}
	lat, err := strconv.ParseFloat(q.Get("lat"), 64)
	if err != nil || lat < -90 || lat > 90 {
		apiErr(w, http.StatusBadRequest, "invalid lat")
		return
	}
	zoom, err := strconv.ParseFloat(q.Get("zoom"), 64)
	if err != nil || zoom < 0 || zoom > 24 {
		apiErr(w, http.StatusBadRequest, "invalid zoom")
		return
	}

	src, ok := s.sets.get(set)
	if !ok {
		apiErr(w, http.StatusNotFound, "unknown set")
		return
	}
	fq, ok := src.(tilesource.FeatureQuerier)
	if !ok {
		apiErr(w, http.StatusBadRequest, "set is not a live tile57 compositor")
		return
	}

	features, err := fq.Query(lon, lat, zoom)
	if err != nil {
		apiErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	type feature struct {
		Class string          `json:"class"`
		Chart string          `json:"chart"`
		S57   json.RawMessage `json:"s57,omitempty"`
	}
	out := struct {
		Set      string    `json:"set"`
		Lon      float64   `json:"lon"`
		Lat      float64   `json:"lat"`
		Zoom     float64   `json:"zoom"`
		Features []feature `json:"features"`
	}{Set: set, Lon: lon, Lat: lat, Zoom: zoom}

	for _, f := range features {
		raw := json.RawMessage(f.S57)
		if !json.Valid(raw) {
			raw, _ = json.Marshal(f.S57)
		}
		out.Features = append(out.Features, feature{Class: f.Class, Chart: f.Chart, S57: raw})
	}

	w.Header().Set("Content-Type", jsonCT)
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(out)
}
