package main

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/redis/go-redis/v9"

	"github.com/ikshantshukla123/crash-safe-object-store/internal/auth"
	"github.com/ikshantshukla123/crash-safe-object-store/internal/coordinator"
	"github.com/ikshantshukla123/crash-safe-object-store/internal/db/dbgen"
	"github.com/ikshantshukla123/crash-safe-object-store/internal/presign"
)

type api struct {
	log    *slog.Logger
	q      *dbgen.Queries
	coord  *coordinator.Coordinator
	issuer *auth.Issuer
	signer *presign.Signer
}

func newRouter(log *slog.Logger, q *dbgen.Queries, rdb *redis.Client, coord *coordinator.Coordinator, issuer *auth.Issuer, signer *presign.Signer) http.Handler {
	a := &api{log: log, q: q, coord: coord, issuer: issuer, signer: signer}

	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(requestLogger(log))
	r.Use(middleware.Recoverer)

	r.Get("/health", handleHealth(log, q, rdb))
	r.Post("/auth/register", a.handleRegister)
	r.Post("/auth/login", a.handleLogin)

	r.Group(func(r chi.Router) {
		r.Use(a.authenticate)
		r.Get("/buckets", a.handleListBuckets)
		r.Post("/buckets", a.handleCreateBucket)
		r.Delete("/buckets/{bucket}", a.handleDeleteBucket)
		r.Get("/buckets/{bucket}/objects", a.handleListObjects)
		r.Put("/buckets/{bucket}/objects/*", a.handlePutObject)
		r.Delete("/buckets/{bucket}/objects/*", a.handleDeleteObject)
		r.Post("/buckets/{bucket}/uploads", a.handleInitUpload)
		r.Put("/buckets/{bucket}/uploads/{uploadID}/parts/{partNo}", a.handleUploadPart)
		r.Post("/buckets/{bucket}/uploads/{uploadID}/complete", a.handleCompleteUpload)
		r.Delete("/buckets/{bucket}/uploads/{uploadID}", a.handleAbortUpload)
		r.Get("/buckets/{bucket}/uploads/{uploadID}/parts", a.handleListParts)
		r.Post("/buckets/{bucket}/presign", a.handlePresign)
	})

	r.Get("/buckets/{bucket}/objects/*", a.handleGetObject)
	return r
}

type ctxKey int

const userIDKey ctxKey = 1

func (a *api) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw := bearerToken(r)
		if raw == "" {
			writeError(w, http.StatusUnauthorized, "missing bearer token")
			return
		}
		sub, err := a.issuer.Subject(raw)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "invalid token")
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), userIDKey, sub)))
	})
}

func bearerToken(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if h == "" {
		return ""
	}
	scheme, token, ok := strings.Cut(h, " ")
	if !ok || !strings.EqualFold(scheme, "bearer") {
		return ""
	}
	return strings.TrimSpace(token)
}

func userID(r *http.Request) string {
	id, _ := r.Context().Value(userIDKey).(string)
	return id
}

func objectKey(r *http.Request) string {
	return strings.TrimPrefix(chi.URLParam(r, "*"), "/")
}

type errorBody struct {
	Error string `json:"error"`
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, errorBody{Error: msg})
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func (a *api) handleError(w http.ResponseWriter, err error) {
	var maxBytesErr *http.MaxBytesError
	switch {
	case errors.Is(err, coordinator.ErrNotFound), errors.Is(err, coordinator.ErrBucketNotFound),
		errors.Is(err, coordinator.ErrUploadNotFound):
		writeError(w, http.StatusNotFound, err.Error())
	case errors.Is(err, coordinator.ErrForbidden):
		writeError(w, http.StatusForbidden, "forbidden")
	case errors.Is(err, coordinator.ErrBucketExists):
		writeError(w, http.StatusConflict, "bucket already exists")
	case errors.Is(err, coordinator.ErrBucketNotEmpty):
		writeError(w, http.StatusConflict, "bucket is not empty")
	case errors.Is(err, coordinator.ErrUploadNotOpen), errors.Is(err, coordinator.ErrUploadCompleted):
		writeError(w, http.StatusConflict, err.Error())
	case errors.Is(err, coordinator.ErrUploadExpired):
		writeError(w, http.StatusGone, "upload expired")
	case errors.Is(err, coordinator.ErrInvalidPart), errors.Is(err, coordinator.ErrInvalidKey),
		errors.Is(err, coordinator.ErrInvalidBucket):
		writeError(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, coordinator.ErrInvalidRange):
		writeError(w, http.StatusRequestedRangeNotSatisfiable, "invalid range")
	case errors.As(err, &maxBytesErr):
		writeError(w, http.StatusRequestEntityTooLarge, "body too large")
	case errors.Is(err, coordinator.ErrTooLarge):
		writeError(w, http.StatusRequestEntityTooLarge, "body too large")
	case errors.Is(err, coordinator.ErrQuorum):
		writeError(w, http.StatusServiceUnavailable, "write quorum not reached")
	case errors.Is(err, coordinator.ErrClientChecksum):
		writeError(w, http.StatusUnprocessableEntity, "client checksum does not match body")
	case errors.Is(err, coordinator.ErrChunkIntegrity):
		writeError(w, http.StatusBadGateway, "chunk integrity check failed")
	default:
		a.log.Error("request failed", "err", err)
		writeError(w, http.StatusInternalServerError, "internal error")
	}
}
