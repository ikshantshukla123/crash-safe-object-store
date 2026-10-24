package main

import (
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
)

type createBucketRequest struct {
	Name string `json:"name"`
}

type bucketResponse struct {
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
}

func (a *api) handleCreateBucket(w http.ResponseWriter, r *http.Request) {
	var in createBucketRequest
	if err := decodeJSON(w, r, &in); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	bucket, err := a.coord.CreateBucket(r.Context(), userID(r), in.Name)
	if err != nil {
		a.handleError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, bucketResponse{Name: bucket.Name, CreatedAt: bucket.CreatedAt.Time})
}

func (a *api) handleListBuckets(w http.ResponseWriter, r *http.Request) {
	buckets, err := a.coord.ListBuckets(r.Context(), userID(r))
	if err != nil {
		a.handleError(w, err)
		return
	}
	out := make([]bucketResponse, 0, len(buckets))
	for _, b := range buckets {
		out = append(out, bucketResponse{Name: b.Name, CreatedAt: b.CreatedAt.Time})
	}
	writeJSON(w, http.StatusOK, map[string]any{"buckets": out})
}

func (a *api) handleDeleteBucket(w http.ResponseWriter, r *http.Request) {
	if err := a.coord.DeleteBucket(r.Context(), userID(r), chi.URLParam(r, "bucket")); err != nil {
		a.handleError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *api) handleListObjects(w http.ResponseWriter, r *http.Request) {
	limit := int32(1000)
	if raw := r.URL.Query().Get("limit"); raw != "" {
		n, err := strconv.ParseInt(raw, 10, 32)
		if err != nil || n < 1 {
			writeError(w, http.StatusBadRequest, "limit must be a positive integer")
			return
		}
		limit = int32(n)
	}
	offset := int32(0)
	if raw := r.URL.Query().Get("offset"); raw != "" {
		n, err := strconv.ParseInt(raw, 10, 32)
		if err != nil || n < 0 {
			writeError(w, http.StatusBadRequest, "offset must be a non-negative integer")
			return
		}
		offset = int32(n)
	}
	rows, err := a.coord.ListObjects(r.Context(), userID(r), chi.URLParam(r, "bucket"), limit, offset)
	if err != nil {
		a.handleError(w, err)
		return
	}
	out := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		out = append(out, map[string]any{
			"key":          row.Key,
			"size_bytes":   row.SizeBytes,
			"etag":         row.Checksum,
			"content_type": row.ContentType,
			"created_at":   row.CreatedAt.Time,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"objects": out})
}
