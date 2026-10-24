package nodeserver

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ikshantshukla123/crash-safe-object-store/internal/objects"
)

func newTestServer(t *testing.T) (*httptest.Server, string) {
	t.Helper()
	root := t.TempDir()
	store, err := objects.NewLocalChunkStore(root)
	if err != nil {
		t.Fatalf("NewLocalChunkStore: %v", err)
	}
	srv := httptest.NewServer(Handler(store, root, slog.New(slog.DiscardHandler)))
	t.Cleanup(srv.Close)
	return srv, root
}

func sha(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func putChunk(t *testing.T, base, id, checksum string, body []byte) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPut, base+"/chunks/"+id, bytes.NewReader(body))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("X-Chunk-SHA256", checksum)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	return resp
}

func TestPutGetDelete(t *testing.T) {
	srv, _ := newTestServer(t)
	data := []byte("chunk bytes")
	id := "0123456789abcdef0123456789abcdef"

	resp := putChunk(t, srv.URL, id, sha(data), data)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT status = %d", resp.StatusCode)
	}

	resp = putChunk(t, srv.URL, id, sha(data), data)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("idempotent PUT status = %d", resp.StatusCode)
	}

	getResp, err := http.Get(srv.URL + "/chunks/" + id)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer getResp.Body.Close()
	got, _ := io.ReadAll(getResp.Body)
	if !bytes.Equal(got, data) {
		t.Errorf("GET = %q, want %q", got, data)
	}

	for i := 0; i < 2; i++ {
		req, _ := http.NewRequest(http.MethodDelete, srv.URL+"/chunks/"+id, nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("delete: %v", err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusNoContent {
			t.Fatalf("DELETE status = %d", resp.StatusCode)
		}
	}
}

func TestPutRejectsConflictingBytes(t *testing.T) {
	srv, _ := newTestServer(t)
	id := "0123456789abcdef0123456789abcdef"
	first := []byte("first")
	resp := putChunk(t, srv.URL, id, sha(first), first)
	resp.Body.Close()

	resp = putChunk(t, srv.URL, id, sha([]byte("different")), []byte("different"))
	resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Errorf("conflicting PUT status = %d, want 409", resp.StatusCode)
	}
}

func TestPutRejectsChecksumMismatch(t *testing.T) {
	srv, _ := newTestServer(t)
	id := "0123456789abcdef0123456789abcdef"
	resp := putChunk(t, srv.URL, id, sha([]byte("promised")), []byte("actual"))
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("mismatched PUT status = %d, want 422", resp.StatusCode)
	}
}

func TestGetMissing(t *testing.T) {
	srv, _ := newTestServer(t)
	resp, err := http.Get(srv.URL + "/chunks/0123456789abcdef0123456789abcdef")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}
}

func TestStats(t *testing.T) {
	srv, _ := newTestServer(t)
	resp, err := http.Get(srv.URL + "/stats")
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("stats status = %d", resp.StatusCode)
	}
	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if _, ok := body["used_bytes"]; !ok {
		t.Error("stats has no used_bytes")
	}
	if _, ok := body["free_bytes"]; !ok {
		t.Error("stats has no free_bytes")
	}
}
