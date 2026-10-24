package objects

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// NodeClient is an HTTP client for one storage node. It implements ChunkStore.
type NodeClient struct {
	ID      string
	Name    string
	BaseURL string
	HTTP    *http.Client
}

var _ ChunkStore = (*NodeClient)(nil)

func NewNodeClient(id, name, baseURL string) *NodeClient {
	return &NodeClient{
		ID:      id,
		Name:    name,
		BaseURL: strings.TrimRight(baseURL, "/"),
		HTTP:    &http.Client{Timeout: 60 * time.Second},
	}
}

func (c *NodeClient) Put(ctx context.Context, id, wantSHA256 string, r io.Reader) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, c.BaseURL+"/chunks/"+id, r)
	if err != nil {
		return fmt.Errorf("build put chunk request: %w", err)
	}
	req.Header.Set("X-Chunk-SHA256", wantSHA256)
	req.Header.Set("Content-Type", "application/octet-stream")

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("put chunk %s to %s: %w", id, c.Name, err)
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK, http.StatusCreated, http.StatusNoContent:
		return nil
	case http.StatusNotFound:
		return fmt.Errorf("%w: %s", ErrChunkNotFound, id)
	case http.StatusConflict, http.StatusUnprocessableEntity:
		return fmt.Errorf("%w: node %s rejected chunk %s", ErrChecksumMismatch, c.Name, id)
	case http.StatusInsufficientStorage:
		return fmt.Errorf("%w: node %s", ErrOutOfSpace, c.Name)
	default:
		return fmt.Errorf("put chunk %s to %s: unexpected status %d", id, c.Name, resp.StatusCode)
	}
}

func (c *NodeClient) Get(ctx context.Context, id string) (io.ReadCloser, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+"/chunks/"+id, nil)
	if err != nil {
		return nil, fmt.Errorf("build get chunk request: %w", err)
	}
	resp, err := c.HTTP.Do(req) //nolint:bodyclose // success body is returned to the caller
	if err != nil {
		return nil, fmt.Errorf("get chunk %s from %s: %w", id, c.Name, err)
	}
	switch resp.StatusCode {
	case http.StatusOK:
		return resp.Body, nil
	case http.StatusNotFound:
		drainClose(resp.Body)
		return nil, fmt.Errorf("%w: %s", ErrChunkNotFound, id)
	default:
		drainClose(resp.Body)
		return nil, fmt.Errorf("get chunk %s from %s: unexpected status %d", id, c.Name, resp.StatusCode)
	}
}

func (c *NodeClient) Delete(ctx context.Context, id string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, c.BaseURL+"/chunks/"+id, nil)
	if err != nil {
		return fmt.Errorf("build delete chunk request: %w", err)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("delete chunk %s from %s: %w", id, c.Name, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	return fmt.Errorf("delete chunk %s from %s: unexpected status %d", id, c.Name, resp.StatusCode)
}

// Healthy reports whether the node answers its health endpoint.
func (c *NodeClient) Healthy(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+"/health", nil)
	if err != nil {
		return err
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("node %s health status %d", c.Name, resp.StatusCode)
	}
	return nil
}

func drainClose(rc io.ReadCloser) {
	_, _ = io.Copy(io.Discard, io.LimitReader(rc, 4096))
	_ = rc.Close()
}

// IsNotFound reports whether an error means the node has no such chunk.
func IsNotFound(err error) bool {
	return errors.Is(err, ErrChunkNotFound)
}

// IsChecksumMismatch reports whether a node rejected the bytes.
func IsChecksumMismatch(err error) bool {
	return errors.Is(err, ErrChecksumMismatch)
}
