package main

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
)

type presignRequest struct {
	Key            string `json:"key"`
	Method         string `json:"method"`
	ExpiresSeconds int64  `json:"expires_seconds"`
}

type presignResponse struct {
	URL       string    `json:"url"`
	ExpiresAt time.Time `json:"expires_at"`
	Method    string    `json:"method"`
}

func (a *api) handlePresign(w http.ResponseWriter, r *http.Request) {
	var in presignRequest
	if err := decodeJSON(w, r, &in); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if in.Key == "" {
		writeError(w, http.StatusBadRequest, "key is required")
		return
	}
	if err := a.coord.CheckBucketOwner(r.Context(), userID(r), chi.URLParam(r, "bucket")); err != nil {
		a.handleError(w, err)
		return
	}
	if in.Method == "" {
		in.Method = http.MethodGet
	}
	if in.Method != http.MethodGet {
		writeError(w, http.StatusBadRequest, "only GET presigned urls are supported")
		return
	}
	if in.ExpiresSeconds <= 0 {
		in.ExpiresSeconds = 3600
	}
	ttl := time.Duration(in.ExpiresSeconds) * time.Second
	path := "/buckets/" + chi.URLParam(r, "bucket") + "/objects/" + in.Key

	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if proto := r.Header.Get("X-Forwarded-Proto"); proto != "" {
		scheme = proto
	}
	signed, err := a.signer.URL(scheme+"://"+r.Host, in.Method, path, "", ttl)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, presignResponse{
		URL:       signed,
		ExpiresAt: time.Now().Add(ttl),
		Method:    in.Method,
	})
}
