package main

import (
	"net/http"
	"strconv"

	"github.com/ikshantshukla123/crash-safe-object-store/internal/search"
)

type searchHit struct {
	Key         string  `json:"key"`
	Bucket      string  `json:"bucket"`
	VersionID   string  `json:"version_id"`
	SizeBytes   int64   `json:"size_bytes"`
	ETag        string  `json:"etag"`
	ContentType string  `json:"content_type"`
	Score       float64 `json:"score"`
	Tier        string  `json:"tier"`
}

func (a *api) handleSearch(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("q")
	if q == "" {
		writeError(w, http.StatusBadRequest, "q is required")
		return
	}
	limit := int64(10)
	if raw := r.URL.Query().Get("limit"); raw != "" {
		n, err := strconv.ParseInt(raw, 10, 32)
		if err != nil || n < 1 || n > 50 {
			writeError(w, http.StatusBadRequest, "limit must be between 1 and 50")
			return
		}
		limit = n
	}
	rows, err := a.searcher.Search(r.Context(), userID(r), q, int32(limit))
	if err != nil {
		a.handleError(w, err)
		return
	}
	out := make([]searchHit, 0, len(rows))
	for _, row := range rows {
		out = append(out, searchHit{
			Key:         row.Key,
			Bucket:      row.BucketName,
			VersionID:   row.VersionID.String(),
			SizeBytes:   row.SizeBytes,
			ETag:        row.Checksum,
			ContentType: row.ContentType,
			Score:       row.Score,
			Tier:        search.Tier(row.Score),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"query": q, "results": out})
}
