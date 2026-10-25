package main

import (
	"bytes"
	"crypto/subtle"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/ikshantshukla123/crash-safe-object-store/internal/auth"
	"github.com/ikshantshukla123/crash-safe-object-store/internal/coordinator"
	"github.com/ikshantshukla123/crash-safe-object-store/internal/db/dbgen"
)

const (
	demoEmail    = "demo@objstore.local"
	demoPassword = "demo-password-123"
	demoBucket   = "demo"
)

func (a *api) requireDemoToken(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if a.demoToken == "" {
			http.NotFound(w, r)
			return
		}
		token := r.Header.Get("X-Demo-Token")
		if token == "" {
			token = r.URL.Query().Get("token")
		}
		if subtle.ConstantTimeCompare([]byte(token), []byte(a.demoToken)) != 1 {
			writeError(w, http.StatusUnauthorized, "invalid demo token")
			return
		}
		next.ServeHTTP(w, r)
	})
}

type demoResponse struct {
	Status   string `json:"status"`
	Objects  int    `json:"objects,omitempty"`
	Bucket   string `json:"bucket,omitempty"`
	Email    string `json:"email,omitempty"`
	Password string `json:"password,omitempty"`
	Token    string `json:"token,omitempty"`
}

func (a *api) handleDemoReset(w http.ResponseWriter, r *http.Request) {
	if err := a.q.ResetMetadata(r.Context()); err != nil {
		a.handleError(w, err)
		return
	}
	if err := a.rdb.FlushDB(r.Context()).Err(); err != nil {
		a.log.Warn("flush redis during demo reset", "err", err)
	}
	if err := a.coord.PurgeChunks(r.Context()); err != nil {
		a.handleError(w, err)
		return
	}
	if err := a.coord.ResetWAL(r.Context()); err != nil {
		a.handleError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, demoResponse{Status: "reset"})
}

func (a *api) handleDemoSeed(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	user, err := a.q.GetUserByEmail(ctx, demoEmail)
	if errors.Is(err, pgx.ErrNoRows) {
		hash, hashErr := auth.HashPassword(demoPassword)
		if hashErr != nil {
			a.handleError(w, hashErr)
			return
		}
		user, err = a.q.CreateUser(ctx, dbgen.CreateUserParams{Email: demoEmail, PasswordHash: hash})
	}
	if err != nil {
		a.handleError(w, err)
		return
	}
	owner := user.ID.String()
	if _, err := a.coord.CreateBucket(ctx, owner, demoBucket); err != nil && !errors.Is(err, coordinator.ErrBucketExists) {
		a.handleError(w, err)
		return
	}

	sizes := []int{512, 1024, 4096, 8192, 16384, 32768, 65536, 131072, 262144, 524288}
	subjects := []string{
		"quarterly revenue report for the north region",
		"customer churn analysis and retention plan",
		"warehouse inventory reconciliation notes",
		"marketing campaign performance summary",
		"platform reliability incident review",
		"annual budget forecast and headcount plan",
		"supplier contract renewal checklist",
		"product roadmap research findings",
		"security audit remediation tracker",
		"logistics routing optimisation study",
	}
	for i := 0; i < 100; i++ {
		key := fmt.Sprintf("reports/2026/%03d-%s.txt", i+1, slug(subjects[i%len(subjects)]))
		body := seedBody(subjects[i%len(subjects)], i, sizes[i%len(sizes)])
		if _, err := a.coord.PutObject(ctx, owner, demoBucket, key, "text/plain; charset=utf-8",
			fmt.Sprintf("demo-seed-%03d", i+1), bytes.NewReader(body)); err != nil {
			a.handleError(w, err)
			return
		}
	}
	token, expiresAt, err := a.issuer.Issue(owner, time.Now())
	if err != nil {
		a.handleError(w, err)
		return
	}
	_ = expiresAt
	writeJSON(w, http.StatusOK, demoResponse{
		Status: "seeded", Objects: 100, Bucket: demoBucket,
		Email: demoEmail, Password: demoPassword, Token: token,
	})
}

func slug(s string) string {
	return strings.ReplaceAll(strings.TrimSpace(s), " ", "-")
}

func seedBody(subject string, index, size int) []byte {
	var buf bytes.Buffer
	for buf.Len() < size {
		fmt.Fprintf(&buf, "Document %03d. %s. This record is part of the crash-safe object store demo dataset. ", index+1, subject)
	}
	return buf.Bytes()[:size]
}
