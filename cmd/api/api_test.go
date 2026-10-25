package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/redis/go-redis/v9"

	"github.com/ikshantshukla123/crash-safe-object-store/internal/auth"
	"github.com/ikshantshukla123/crash-safe-object-store/internal/coordinator"
	"github.com/ikshantshukla123/crash-safe-object-store/internal/db/dbgen"
	"github.com/ikshantshukla123/crash-safe-object-store/internal/metrics"
	"github.com/ikshantshukla123/crash-safe-object-store/internal/nodeserver"
	"github.com/ikshantshukla123/crash-safe-object-store/internal/objects"
	"github.com/ikshantshukla123/crash-safe-object-store/internal/placement"
	"github.com/ikshantshukla123/crash-safe-object-store/internal/presign"
	"github.com/ikshantshukla123/crash-safe-object-store/internal/search"
	"github.com/ikshantshukla123/crash-safe-object-store/internal/testutil"
)

type apiHarness struct {
	server *httptest.Server
	coord  *coordinator.Coordinator
}

func newAPIHarness(t *testing.T) *apiHarness {
	t.Helper()
	db := testutil.NewDB(t)
	q := dbgen.New(db.Pool)
	ctx := context.Background()
	log := slog.New(slog.DiscardHandler)

	var nodes []placement.Node
	for i := 1; i <= 3; i++ {
		name := fmt.Sprintf("node%d", i)
		root := t.TempDir()
		store, err := objects.NewLocalChunkStore(root)
		if err != nil {
			t.Fatalf("local store: %v", err)
		}
		srv := httptest.NewServer(nodeserver.Handler(store, root, log))
		t.Cleanup(srv.Close)
		id := placement.NodeID(name)
		if _, err := q.UpsertStorageNode(ctx, dbgen.UpsertStorageNodeParams{
			ID:      mustPGUUID(t, id),
			Name:    name,
			BaseUrl: srv.URL,
		}); err != nil {
			t.Fatalf("register node: %v", err)
		}
		nodes = append(nodes, placement.Node{ID: id, Name: name, Status: "up", Store: objects.NewNodeClient(id, name, srv.URL)})
	}

	coord, err := coordinator.New(db.Pool, q, log, nil, nodes, coordinator.Config{
		Replicas: 3, WriteQuorum: 2, MaxPartSize: 16 << 20, MultipartExpiry: time.Hour,
	})
	if err != nil {
		t.Fatalf("coordinator: %v", err)
	}
	issuer, err := auth.NewIssuer(strings.Repeat("j", 32), 15*time.Minute)
	if err != nil {
		t.Fatalf("issuer: %v", err)
	}
	signer, err := presign.New("k1", strings.Repeat("p", 32), 7*24*time.Hour)
	if err != nil {
		t.Fatalf("signer: %v", err)
	}
	rdb := redis.NewClient(&redis.Options{Addr: "127.0.0.1:1"})
	t.Cleanup(func() { rdb.Close() })

	embedder, err := search.NewEmbedder("local-hash-1536", "", 1536)
	if err != nil {
		t.Fatalf("embedder: %v", err)
	}
	h := &apiHarness{coord: coord}
	h.server = httptest.NewServer(newRouter(log, q, rdb, coord, issuer, signer, metrics.New(), search.NewSearcher(q, embedder), "test-demo-token"))
	t.Cleanup(h.server.Close)
	return h
}

func (h *apiHarness) do(t *testing.T, method, path, token string, body []byte, headers map[string]string) *http.Response {
	t.Helper()
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequest(method, h.server.URL+path, rdr)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	return resp
}

func (h *apiHarness) doJSON(t *testing.T, method, path, token string, body any, out any) *http.Response {
	t.Helper()
	var raw []byte
	if body != nil {
		var err error
		raw, err = json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal body: %v", err)
		}
	}
	resp := h.do(t, method, path, token, raw, map[string]string{"Content-Type": "application/json"}) //nolint:bodyclose // helper closes the body
	defer resp.Body.Close()
	if out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			t.Fatalf("decode %s %s: %v", method, path, err)
		}
	}
	return resp
}

func mustPGUUID(t *testing.T, s string) pgtype.UUID {
	t.Helper()
	var u pgtype.UUID
	if err := u.Scan(s); err != nil {
		t.Fatalf("parse uuid %s: %v", s, err)
	}
	return u
}

func register(t *testing.T, h *apiHarness, email string) (token, id string) {
	t.Helper()
	var out tokenResponse
	resp := h.doJSON(t, http.MethodPost, "/auth/register", "", credentials{Email: email, Password: "password123"}, &out) //nolint:bodyclose // doJSON closes it
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("register status = %d", resp.StatusCode)
	}
	return out.Token, out.UserID
}

func TestAPIObjectLifecycle(t *testing.T) {
	h := newAPIHarness(t)
	token, _ := register(t, h, "owner@example.com")

	if resp := h.doJSON(t, http.MethodPost, "/buckets", token, createBucketRequest{Name: "docs"}, nil); resp.StatusCode != http.StatusCreated { //nolint:bodyclose // doJSON closes it
		t.Fatalf("create bucket status = %d", resp.StatusCode)
	}
	if resp := h.doJSON(t, http.MethodPost, "/buckets", token, createBucketRequest{Name: "docs"}, nil); resp.StatusCode != http.StatusConflict { //nolint:bodyclose // doJSON closes it
		t.Errorf("duplicate bucket status = %d, want 409", resp.StatusCode)
	}
	if resp := h.doJSON(t, http.MethodPost, "/buckets", "", createBucketRequest{Name: "nope"}, nil); resp.StatusCode != http.StatusUnauthorized { //nolint:bodyclose // doJSON closes it
		t.Errorf("unauthenticated create status = %d, want 401", resp.StatusCode)
	}

	body := []byte("hello range world")
	var put objectResponse
	resp := h.do(t, http.MethodPut, "/buckets/docs/objects/hello.txt", token, body, map[string]string{ //nolint:bodyclose // helper closes the body
		"Content-Type": "text/plain", "Idempotency-Key": "put-1",
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("put status = %d: %s", resp.StatusCode, readBody(resp))
	}
	json.NewDecoder(resp.Body).Decode(&put)
	resp.Body.Close()

	var again objectResponse
	resp = h.do(t, http.MethodPut, "/buckets/docs/objects/hello.txt", token, body, map[string]string{"Idempotency-Key": "put-1"}) //nolint:bodyclose // helper closes the body
	json.NewDecoder(resp.Body).Decode(&again)
	resp.Body.Close()
	if again.VersionID != put.VersionID {
		t.Errorf("retried put created a new version: %s then %s", put.VersionID, again.VersionID)
	}

	resp = h.do(t, http.MethodGet, "/buckets/docs/objects/hello.txt", token, nil, nil) //nolint:bodyclose // helper closes the body
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get status = %d", resp.StatusCode)
	}
	if got := readBody(resp); got != string(body) {
		t.Errorf("get = %q, want %q", got, body)
	}
	if resp.Header.Get("Accept-Ranges") != "bytes" {
		t.Errorf("Accept-Ranges = %q", resp.Header.Get("Accept-Ranges"))
	}

	resp = h.do(t, http.MethodGet, "/buckets/docs/objects/hello.txt", token, nil, map[string]string{"Range": "bytes=6-10"}) //nolint:bodyclose // helper closes the body
	if resp.StatusCode != http.StatusPartialContent {
		t.Fatalf("range status = %d, want 206", resp.StatusCode)
	}
	if got := readBody(resp); got != "range" {
		t.Errorf("range body = %q, want %q", got, "range")
	}
	if cr := resp.Header.Get("Content-Range"); cr != "bytes 6-10/17" {
		t.Errorf("Content-Range = %q", cr)
	}

	resp = h.do(t, http.MethodGet, "/buckets/docs/objects/hello.txt", token, nil, map[string]string{"Range": "bytes=999-"}) //nolint:bodyclose // status only
	if resp.StatusCode != http.StatusRequestedRangeNotSatisfiable {
		t.Errorf("invalid range status = %d, want 416", resp.StatusCode)
	}

	if resp := h.do(t, http.MethodGet, "/buckets/docs/objects/hello.txt", "", nil, nil); resp.StatusCode != http.StatusUnauthorized { //nolint:bodyclose // status only
		t.Errorf("anonymous get status = %d, want 401", resp.StatusCode)
	}

	var presigned presignResponse
	resp = h.doJSON(t, http.MethodPost, "/buckets/docs/presign", token, presignRequest{Key: "hello.txt"}, &presigned) //nolint:bodyclose // doJSON closes it
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("presign status = %d", resp.StatusCode)
	}
	pr, err := http.Get(presigned.URL)
	if err != nil {
		t.Fatalf("presigned get: %v", err)
	}
	if pr.StatusCode != http.StatusOK || readBody(pr) != string(body) {
		t.Errorf("presigned get status = %d", pr.StatusCode)
	}

	otherToken, _ := register(t, h, "other@example.com")
	if resp := h.do(t, http.MethodGet, "/buckets/docs/objects/hello.txt", otherToken, nil, nil); resp.StatusCode != http.StatusForbidden { //nolint:bodyclose // status only
		t.Errorf("other owner get status = %d, want 403", resp.StatusCode)
	}

	if resp := h.doJSON(t, http.MethodDelete, "/buckets/docs", token, nil, nil); resp.StatusCode != http.StatusConflict { //nolint:bodyclose // doJSON closes it
		t.Errorf("delete non-empty bucket status = %d, want 409", resp.StatusCode)
	}
	if resp := h.do(t, http.MethodDelete, "/buckets/docs/objects/hello.txt", token, nil, nil); resp.StatusCode != http.StatusNoContent { //nolint:bodyclose // status only
		t.Errorf("delete status = %d", resp.StatusCode)
	}
	if resp := h.do(t, http.MethodDelete, "/buckets/docs/objects/hello.txt", token, nil, nil); resp.StatusCode != http.StatusNoContent { //nolint:bodyclose // status only
		t.Errorf("repeated delete status = %d, want 204", resp.StatusCode)
	}
	if resp := h.do(t, http.MethodGet, "/buckets/docs/objects/hello.txt", token, nil, nil); resp.StatusCode != http.StatusNotFound { //nolint:bodyclose // status only
		t.Errorf("get deleted status = %d, want 404", resp.StatusCode)
	}
	if resp := h.doJSON(t, http.MethodDelete, "/buckets/docs", token, nil, nil); resp.StatusCode != http.StatusNoContent { //nolint:bodyclose // doJSON closes it
		t.Errorf("delete bucket status = %d", resp.StatusCode)
	}
	if resp := h.doJSON(t, http.MethodDelete, "/buckets/docs", token, nil, nil); resp.StatusCode != http.StatusNoContent { //nolint:bodyclose // doJSON closes it
		t.Errorf("repeated delete bucket status = %d, want 204", resp.StatusCode)
	}
}

func TestAPIMultipart(t *testing.T) {
	h := newAPIHarness(t)
	token, _ := register(t, h, "multipart@example.com")
	if resp := h.doJSON(t, http.MethodPost, "/buckets", token, createBucketRequest{Name: "mpbucket"}, nil); resp.StatusCode != http.StatusCreated { //nolint:bodyclose // doJSON closes it
		t.Fatalf("create bucket status = %d", resp.StatusCode)
	}

	var init initUploadResponse
	resp := h.doJSON(t, http.MethodPost, "/buckets/mpbucket/uploads", token, initUploadRequest{Key: "big.bin"}, &init) //nolint:bodyclose // doJSON closes it
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("init upload status = %d", resp.StatusCode)
	}
	parts := [][]byte{bytes.Repeat([]byte("x"), 1024), bytes.Repeat([]byte("y"), 512)}
	for i, part := range parts {
		resp = h.do(t, http.MethodPut, fmt.Sprintf("/buckets/mpbucket/uploads/%s/parts/%d", init.UploadID, i+1), token, part, nil) //nolint:bodyclose // helper closes the body
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("upload part %d status = %d", i+1, resp.StatusCode)
		}
		resp.Body.Close()
	}

	resp = h.do(t, http.MethodPut, fmt.Sprintf("/buckets/mpbucket/uploads/%s/parts/3", init.UploadID), token, []byte("bad"), map[string]string{"X-Chunk-SHA256": strings.Repeat("0", 64)})
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("bad part checksum status = %d, want 422", resp.StatusCode)
	}
	resp.Body.Close()

	var listed struct {
		Parts []partResponse `json:"parts"`
	}
	if resp := h.doJSON(t, http.MethodGet, "/buckets/mpbucket/uploads/"+init.UploadID+"/parts", token, nil, &listed); resp.StatusCode != http.StatusOK || len(listed.Parts) != 2 { //nolint:bodyclose // helper closes the body
		t.Fatalf("list parts status = %d, parts = %d", resp.StatusCode, len(listed.Parts))
	}

	var completed objectResponse
	resp = h.doJSON(t, http.MethodPost, "/buckets/mpbucket/uploads/"+init.UploadID+"/complete", token, completeUploadRequest{Parts: []int32{1, 2}}, &completed) //nolint:bodyclose // helper closes the body
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("complete status = %d", resp.StatusCode)
	}
	want := append(append([]byte{}, parts[0]...), parts[1]...)
	resp = h.do(t, http.MethodGet, "/buckets/mpbucket/objects/big.bin", token, nil, nil) //nolint:bodyclose // helper closes the body
	if resp.StatusCode != http.StatusOK || !bytes.Equal([]byte(readBody(resp)), want) {
		t.Errorf("multipart get status = %d", resp.StatusCode)
	}

	var uploads struct {
		Objects []struct {
			Key string `json:"key"`
		} `json:"objects"`
	}
	if resp := h.doJSON(t, http.MethodGet, "/buckets/mpbucket/objects", token, nil, &uploads); resp.StatusCode != http.StatusOK || len(uploads.Objects) != 1 { //nolint:bodyclose // helper closes the body
		t.Errorf("list objects status = %d, objects = %d", resp.StatusCode, len(uploads.Objects))
	}
}

func TestAPIAuth(t *testing.T) {
	h := newAPIHarness(t)
	token, _ := register(t, h, "auth@example.com")
	_ = token

	var login tokenResponse
	if resp := h.doJSON(t, http.MethodPost, "/auth/login", "", credentials{Email: "auth@example.com", Password: "password123"}, &login); resp.StatusCode != http.StatusOK || login.Token == "" { //nolint:bodyclose // helper closes the body
		t.Fatalf("login status = %d", resp.StatusCode)
	}
	if resp := h.doJSON(t, http.MethodPost, "/auth/login", "", credentials{Email: "auth@example.com", Password: "wrong-password"}, nil); resp.StatusCode != http.StatusUnauthorized { //nolint:bodyclose // helper closes the body
		t.Errorf("bad login status = %d, want 401", resp.StatusCode)
	}
	if resp := h.doJSON(t, http.MethodPost, "/auth/register", "", credentials{Email: "auth@example.com", Password: "password123"}, nil); resp.StatusCode != http.StatusConflict { //nolint:bodyclose // helper closes the body
		t.Errorf("duplicate register status = %d, want 409", resp.StatusCode)
	}
	if resp := h.doJSON(t, http.MethodPost, "/auth/register", "", credentials{Email: "x@example.com", Password: "short"}, nil); resp.StatusCode != http.StatusBadRequest { //nolint:bodyclose // helper closes the body
		t.Errorf("short password status = %d, want 400", resp.StatusCode)
	}
}

func readBody(resp *http.Response) string {
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return string(b)
}

func TestAPIVersionsAndRollback(t *testing.T) {
	h := newAPIHarness(t)
	token, _ := register(t, h, "versions@example.com")
	if resp := h.doJSON(t, http.MethodPost, "/buckets", token, createBucketRequest{Name: "history"}, nil); resp.StatusCode != http.StatusCreated { //nolint:bodyclose // doJSON closes it
		t.Fatalf("create bucket status = %d", resp.StatusCode)
	}
	put := func(body, idem string) objectResponse {
		t.Helper()
		resp := h.do(t, http.MethodPut, "/buckets/history/objects/doc.txt", token, []byte(body), map[string]string{ //nolint:bodyclose // helper closes the body
			"Content-Type": "text/plain", "Idempotency-Key": idem,
		})
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("put %s status = %d: %s", idem, resp.StatusCode, readBody(resp))
		}
		var out objectResponse
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			t.Fatalf("decode put: %v", err)
		}
		return out
	}
	first := put("version one", "v1")
	second := put("version two", "v2")

	var list struct {
		Versions []versionResponse `json:"versions"`
	}
	if resp := h.doJSON(t, http.MethodGet, "/buckets/history/versions?key=doc.txt", token, nil, &list); resp.StatusCode != http.StatusOK { //nolint:bodyclose // doJSON closes it
		t.Fatalf("versions status = %d", resp.StatusCode)
	}
	if len(list.Versions) != 2 || list.Versions[0].VersionID != second.VersionID || list.Versions[1].VersionID != first.VersionID {
		t.Fatalf("versions = %+v", list.Versions)
	}
	for i := 0; i < 2; i++ {
		if resp := h.doJSON(t, http.MethodPost, "/buckets/history/rollback", token, rollbackRequest{Key: "doc.txt", VersionID: first.VersionID}, nil); resp.StatusCode != http.StatusOK { //nolint:bodyclose // doJSON closes it
			t.Fatalf("rollback %d status = %d", i, resp.StatusCode)
		}
		resp := h.do(t, http.MethodGet, "/buckets/history/objects/doc.txt", token, nil, nil) //nolint:bodyclose // helper closes the body
		if resp.StatusCode != http.StatusOK || readBody(resp) != "version one" {
			t.Fatalf("after rollback %d status=%d body mismatch", i, resp.StatusCode)
		}
	}
	if resp := h.doJSON(t, http.MethodPost, "/buckets/history/rollback", token, rollbackRequest{Key: "doc.txt", VersionID: uuid.NewString()}, nil); resp.StatusCode != http.StatusNotFound { //nolint:bodyclose // doJSON closes it
		t.Fatalf("unknown version rollback status = %d, want 404", resp.StatusCode)
	}
}
