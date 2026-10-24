package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/ikshantshukla123/crash-safe-object-store/internal/auth"
	"github.com/ikshantshukla123/crash-safe-object-store/internal/db/dbgen"
)

type credentials struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

type tokenResponse struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
	UserID    string    `json:"user_id"`
}

func (a *api) handleRegister(w http.ResponseWriter, r *http.Request) {
	var in credentials
	if err := decodeJSON(w, r, &in); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	in.Email = strings.TrimSpace(strings.ToLower(in.Email))
	if !strings.Contains(in.Email, "@") || len(in.Email) > 320 {
		writeError(w, http.StatusBadRequest, "valid email required")
		return
	}
	if len(in.Password) < 8 {
		writeError(w, http.StatusBadRequest, "password must be at least 8 characters")
		return
	}

	hash, err := auth.HashPassword(in.Password)
	if err != nil {
		a.handleError(w, err)
		return
	}
	user, err := a.q.CreateUser(r.Context(), dbgen.CreateUserParams{Email: in.Email, PasswordHash: hash})
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusConflict, "email already registered")
		return
	}
	if err != nil {
		a.handleError(w, err)
		return
	}
	a.issueToken(w, user.ID.String())
}

func (a *api) handleLogin(w http.ResponseWriter, r *http.Request) {
	var in credentials
	if err := decodeJSON(w, r, &in); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	user, err := a.q.GetUserByEmail(r.Context(), strings.TrimSpace(strings.ToLower(in.Email)))
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusUnauthorized, "invalid email or password")
		return
	}
	if err != nil {
		a.handleError(w, err)
		return
	}
	if err := auth.VerifyPassword(in.Password, user.PasswordHash); err != nil {
		writeError(w, http.StatusUnauthorized, "invalid email or password")
		return
	}
	a.issueToken(w, user.ID.String())
}

func (a *api) issueToken(w http.ResponseWriter, userID string) {
	token, expires, err := a.issuer.Issue(userID, time.Now())
	if err != nil {
		a.handleError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, tokenResponse{Token: token, ExpiresAt: expires, UserID: userID})
}

func decodeJSON(w http.ResponseWriter, r *http.Request, v any) error {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return errors.New("invalid JSON body")
	}
	return nil
}
