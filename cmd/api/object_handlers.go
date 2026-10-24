package main

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/ikshantshukla123/crash-safe-object-store/internal/coordinator"
	"github.com/ikshantshukla123/crash-safe-object-store/internal/presign"
)

type objectResponse struct {
	Key       string    `json:"key"`
	VersionID string    `json:"version_id"`
	ETag      string    `json:"etag"`
	SizeBytes int64     `json:"size_bytes"`
	CreatedAt time.Time `json:"created_at"`
}

func (a *api) handlePutObject(w http.ResponseWriter, r *http.Request) {
	bucket := chi.URLParam(r, "bucket")
	key := objectKey(r)
	r.Body = http.MaxBytesReader(w, r.Body, 16<<20)
	version, err := a.coord.PutObject(
		r.Context(),
		userID(r),
		bucket,
		key,
		r.Header.Get("Content-Type"),
		r.Header.Get("Idempotency-Key"),
		r.Body,
	)
	if err != nil {
		a.handleError(w, err)
		return
	}
	w.Header().Set("ETag", version.Checksum)
	writeJSON(w, http.StatusOK, objectResponse{
		Key:       key,
		VersionID: version.ID.String(),
		ETag:      version.Checksum,
		SizeBytes: version.SizeBytes,
		CreatedAt: version.CreatedAt.Time,
	})
}

func (a *api) handleGetObject(w http.ResponseWriter, r *http.Request) {
	bucket := chi.URLParam(r, "bucket")
	key := objectKey(r)

	var (
		reader *coordinator.ObjectReader
		err    error
	)
	if token := bearerToken(r); token != "" {
		sub, tokenErr := a.issuer.Subject(token)
		if tokenErr != nil {
			writeError(w, http.StatusUnauthorized, "invalid token")
			return
		}
		reader, err = a.coord.GetObjectRange(r.Context(), sub, bucket, key, r.Header.Get("Range"))
	} else {
		if verifyErr := a.signer.Verify(r, ""); verifyErr != nil {
			if errors.Is(verifyErr, presign.ErrExpired) {
				writeError(w, http.StatusForbidden, "presigned url expired")
				return
			}
			writeError(w, http.StatusUnauthorized, "authentication required")
			return
		}
		reader, err = a.coord.GetObjectPublicRange(r.Context(), bucket, key, r.Header.Get("Range"))
	}
	if err != nil {
		a.handleError(w, err)
		return
	}
	defer reader.Close()

	w.Header().Set("Content-Type", reader.ContentType)
	w.Header().Set("Content-Length", strconv.FormatInt(reader.Size, 10))
	w.Header().Set("ETag", reader.Version.Checksum)
	w.Header().Set("X-Version-Id", reader.Version.ID.String())
	w.Header().Set("Accept-Ranges", "bytes")
	status := http.StatusOK
	if reader.Partial {
		status = http.StatusPartialContent
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", reader.Start, reader.End, reader.FullSize))
	}
	w.WriteHeader(status)
	if _, err := io.Copy(w, reader); err != nil && !errors.Is(err, r.Context().Err()) {
		a.log.WarnContext(r.Context(), "streaming object", "key", key, "err", err)
	}
}

func (a *api) handleDeleteObject(w http.ResponseWriter, r *http.Request) {
	if err := a.coord.DeleteObject(r.Context(), userID(r), chi.URLParam(r, "bucket"), objectKey(r)); err != nil {
		a.handleError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
