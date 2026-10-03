package main

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5/middleware"
)

// requestLogger emits one structured line per request.
//
// Every line carries op_id, taken from chi's RequestID. CLAUDE.md §14 requires
// that a single write can be traced across the API, the WAL and the node logs,
// and a shared op_id is what makes that possible.
func requestLogger(log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)

			next.ServeHTTP(ww, r)

			level := slog.LevelInfo
			if ww.Status() >= http.StatusInternalServerError {
				level = slog.LevelError
			}

			log.LogAttrs(r.Context(), level, "http request",
				slog.String("op_id", middleware.GetReqID(r.Context())),
				slog.String("method", r.Method),
				slog.String("path", r.URL.Path),
				slog.Int("status", ww.Status()),
				slog.Int("bytes", ww.BytesWritten()),
				slog.Duration("took", time.Since(start)),
			)
		})
	}
}
