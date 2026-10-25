// Package nodeserver exposes a storage node's chunk directory over HTTP.
package nodeserver

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"syscall"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/ikshantshukla123/crash-safe-object-store/internal/objects"
)

const maxChunkBytes = 16 << 20

// Handler serves /chunks, /health and /stats for one node.
func Handler(store objects.ChunkStore, dataRoot string, log *slog.Logger) http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.Recoverer)

	r.Get("/health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	r.Get("/stats", func(w http.ResponseWriter, r *http.Request) {
		used, free, err := diskUsage(dataRoot)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"used_bytes": used,
			"free_bytes": free,
		})
	})

	r.Get("/chunks", func(w http.ResponseWriter, r *http.Request) {
		lister, ok := store.(objects.Lister)
		if !ok {
			writeJSON(w, http.StatusNotImplemented, map[string]string{"error": "listing not supported"})
			return
		}
		chunks, err := lister.List(r.Context())
		if err != nil {
			log.ErrorContext(r.Context(), "list chunks", "err", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"chunks": chunks})
	})

	r.Delete("/chunks", func(w http.ResponseWriter, r *http.Request) {
		purger, ok := store.(objects.Purger)
		if !ok {
			writeJSON(w, http.StatusNotImplemented, map[string]string{"error": "purge not supported"})
			return
		}
		if err := purger.PurgeAll(r.Context()); err != nil {
			log.ErrorContext(r.Context(), "purge chunks", "err", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})

	r.Put("/chunks/{id}", func(w http.ResponseWriter, r *http.Request) {
		id := chi.URLParam(r, "id")
		want := r.Header.Get("X-Chunk-SHA256")
		if len(want) != 64 {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "X-Chunk-SHA256 must be 64 hex characters"})
			return
		}

		existing, err := hashExisting(r, store, id)
		if err != nil && !objects.IsNotFound(err) {
			log.WarnContext(r.Context(), "checking existing chunk", "chunk_id", id, "err", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
			return
		}
		if err == nil {
			if existing != want {
				writeJSON(w, http.StatusConflict, map[string]string{"error": "chunk id already holds different bytes"})
				return
			}
			writeJSON(w, http.StatusOK, map[string]string{"id": id, "sha256": want})
			return
		}

		body := http.MaxBytesReader(w, r.Body, maxChunkBytes+1)
		err = store.Put(r.Context(), id, want, body)
		switch {
		case err == nil:
			writeJSON(w, http.StatusOK, map[string]string{"id": id, "sha256": want})
		case objects.IsChecksumMismatch(err):
			writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": "checksum mismatch"})
		case errors.Is(err, objects.ErrOutOfSpace), errors.Is(err, syscall.ENOSPC):
			writeJSON(w, http.StatusInsufficientStorage, map[string]string{"error": "out of space"})
		default:
			var maxErr *http.MaxBytesError
			if errors.As(err, &maxErr) {
				writeJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"error": "chunk exceeds 16 MiB"})
				return
			}
			log.ErrorContext(r.Context(), "put chunk", "chunk_id", id, "err", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		}
	})

	r.Get("/chunks/{id}", func(w http.ResponseWriter, r *http.Request) {
		id := chi.URLParam(r, "id")
		rc, err := store.Get(r.Context(), id)
		if err != nil {
			if objects.IsNotFound(err) {
				writeJSON(w, http.StatusNotFound, map[string]string{"error": "chunk not found"})
				return
			}
			log.ErrorContext(r.Context(), "get chunk", "chunk_id", id, "err", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
			return
		}
		defer rc.Close()
		w.Header().Set("Content-Type", "application/octet-stream")
		if _, err := io.Copy(w, rc); err != nil {
			log.WarnContext(r.Context(), "streaming chunk", "chunk_id", id, "err", err)
		}
	})

	r.Delete("/chunks/{id}", func(w http.ResponseWriter, r *http.Request) {
		id := chi.URLParam(r, "id")
		if err := store.Delete(r.Context(), id); err != nil {
			log.ErrorContext(r.Context(), "delete chunk", "chunk_id", id, "err", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})

	return r
}

func hashExisting(r *http.Request, store objects.ChunkStore, id string) (string, error) {
	rc, err := store.Get(r.Context(), id)
	if err != nil {
		return "", err
	}
	defer rc.Close()
	h := sha256.New()
	if _, err := io.Copy(h, io.LimitReader(rc, maxChunkBytes+1)); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func diskUsage(path string) (used, free uint64, err error) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return 0, 0, err
	}
	bsize := uint64(stat.Bsize)
	used = (stat.Blocks - stat.Bfree) * bsize
	free = stat.Bavail * bsize
	return used, free, nil
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
