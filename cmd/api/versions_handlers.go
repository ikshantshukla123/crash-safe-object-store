package main

import (
	"net/http"

	"github.com/go-chi/chi/v5"
)

type rollbackRequest struct {
	Key       string `json:"key"`
	VersionID string `json:"version_id"`
}

type versionResponse struct {
	VersionID      string `json:"version_id"`
	VersionNo      int64  `json:"version_no"`
	SizeBytes      int64  `json:"size_bytes"`
	Checksum       string `json:"checksum"`
	ContentType    string `json:"content_type"`
	IsDeleteMarker bool   `json:"is_delete_marker"`
	CreatedAt      string `json:"created_at"`
}

func (a *api) handleListVersions(w http.ResponseWriter, r *http.Request) {
	key := r.URL.Query().Get("key")
	if key == "" {
		writeError(w, http.StatusBadRequest, "key is required")
		return
	}
	versions, err := a.coord.ListVersions(r.Context(), userID(r), chi.URLParam(r, "bucket"), key)
	if err != nil {
		a.handleError(w, err)
		return
	}
	out := make([]versionResponse, 0, len(versions))
	for _, v := range versions {
		out = append(out, versionResponse{
			VersionID:      v.ID.String(),
			VersionNo:      v.VersionNo,
			SizeBytes:      v.SizeBytes,
			Checksum:       v.Checksum,
			ContentType:    v.ContentType,
			IsDeleteMarker: v.IsDeleteMarker,
			CreatedAt:      v.CreatedAt.Time.UTC().Format("2006-01-02T15:04:05Z"),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"versions": out})
}

func (a *api) handleRollback(w http.ResponseWriter, r *http.Request) {
	var in rollbackRequest
	if err := decodeJSON(w, r, &in); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	version, err := a.coord.Rollback(r.Context(), userID(r), chi.URLParam(r, "bucket"), in.Key, in.VersionID)
	if err != nil {
		a.handleError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, objectResponse{
		Key:       in.Key,
		VersionID: version.ID.String(),
		ETag:      version.Checksum,
		SizeBytes: version.SizeBytes,
		CreatedAt: version.CreatedAt.Time,
	})
}
