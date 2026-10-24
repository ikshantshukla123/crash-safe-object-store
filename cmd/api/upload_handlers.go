package main

import (
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
)

type initUploadRequest struct {
	Key         string `json:"key"`
	ContentType string `json:"content_type"`
}

type initUploadResponse struct {
	UploadID  string    `json:"upload_id"`
	Key       string    `json:"key"`
	ExpiresAt time.Time `json:"expires_at"`
}

type completeUploadRequest struct {
	Parts []int32 `json:"parts"`
}

type partResponse struct {
	PartNo    int32  `json:"part_no"`
	SizeBytes int64  `json:"size_bytes"`
	SHA256    string `json:"sha256"`
	ETag      string `json:"etag"`
}

func (a *api) handleInitUpload(w http.ResponseWriter, r *http.Request) {
	var in initUploadRequest
	if err := decodeJSON(w, r, &in); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	upload, err := a.coord.StartUpload(
		r.Context(),
		userID(r),
		chi.URLParam(r, "bucket"),
		in.Key,
		in.ContentType,
		r.Header.Get("Idempotency-Key"),
	)
	if err != nil {
		a.handleError(w, err)
		return
	}
	if upload.Status == "completed" {
		writeError(w, http.StatusConflict, "upload already completed")
		return
	}
	writeJSON(w, http.StatusCreated, initUploadResponse{
		UploadID:  upload.ID.String(),
		Key:       upload.Key,
		ExpiresAt: upload.ExpiresAt.Time,
	})
}

func (a *api) handleUploadPart(w http.ResponseWriter, r *http.Request) {
	partNo, err := strconv.ParseInt(chi.URLParam(r, "partNo"), 10, 32)
	if err != nil || partNo < 1 {
		writeError(w, http.StatusBadRequest, "part number must be a positive integer")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 16<<20)
	sha, err := a.coord.UploadPart(
		r.Context(),
		userID(r),
		chi.URLParam(r, "bucket"),
		chi.URLParam(r, "uploadID"),
		int32(partNo),
		r.Body,
		r.Header.Get("X-Chunk-SHA256"),
	)
	if err != nil {
		a.handleError(w, err)
		return
	}
	w.Header().Set("ETag", sha)
	writeJSON(w, http.StatusOK, map[string]string{"etag": sha})
}

func (a *api) handleCompleteUpload(w http.ResponseWriter, r *http.Request) {
	var in completeUploadRequest
	if r.ContentLength > 0 {
		if err := decodeJSON(w, r, &in); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	version, err := a.coord.CompleteUpload(
		r.Context(),
		userID(r),
		chi.URLParam(r, "bucket"),
		chi.URLParam(r, "uploadID"),
		in.Parts,
	)
	if err != nil {
		a.handleError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, objectResponse{
		VersionID: version.ID.String(),
		ETag:      version.Checksum,
		SizeBytes: version.SizeBytes,
		CreatedAt: version.CreatedAt.Time,
	})
}

func (a *api) handleAbortUpload(w http.ResponseWriter, r *http.Request) {
	err := a.coord.AbortUpload(
		r.Context(),
		userID(r),
		chi.URLParam(r, "bucket"),
		chi.URLParam(r, "uploadID"),
	)
	if err != nil {
		a.handleError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *api) handleListParts(w http.ResponseWriter, r *http.Request) {
	parts, err := a.coord.ListUploadParts(
		r.Context(),
		userID(r),
		chi.URLParam(r, "bucket"),
		chi.URLParam(r, "uploadID"),
	)
	if err != nil {
		a.handleError(w, err)
		return
	}
	out := make([]partResponse, 0, len(parts))
	for _, p := range parts {
		out = append(out, partResponse{
			PartNo:    p.PartNo,
			SizeBytes: p.SizeBytes,
			SHA256:    p.Sha256,
			ETag:      p.Sha256,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"parts": out})
}
