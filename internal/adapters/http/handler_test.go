package httpadapter

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/amurru/lesac/internal/adapters/metadata/sqlite"
	"github.com/amurru/lesac/internal/adapters/storage/filesystem"
	"github.com/amurru/lesac/internal/domain"
	"github.com/amurru/lesac/internal/ports"
	"github.com/amurru/lesac/internal/usecase"
)

func TestPutGetDeleteIntegration(t *testing.T) {
	temp := t.TempDir()
	metadataStore, err := sqlite.NewStore(filepath.Join(temp, "lesac.db"))
	if err != nil {
		t.Fatalf("new sqlite store failed: %v", err)
	}
	defer metadataStore.Close()

	blobStore, err := filesystem.NewStore(filepath.Join(temp, "files"))
	if err != nil {
		t.Fatalf("new filesystem store failed: %v", err)
	}

	service := usecase.NewService(metadataStore, blobStore, nil, nil)
	server := httptest.NewServer(NewHandler(service, 1024))
	defer server.Close()

	putResp, err := http.DefaultClient.Do(
		mustRequest(http.MethodPut, server.URL+"/v1/files", []byte("hello")),
	)
	if err != nil {
		t.Fatalf("put request failed: %v", err)
	}
	defer putResp.Body.Close()

	if putResp.StatusCode != http.StatusCreated {
		t.Fatalf("expected 201, got %d", putResp.StatusCode)
	}

	var putBody struct {
		ID  string `json:"id"`
		URL string `json:"url"`
	}
	if err := json.NewDecoder(putResp.Body).Decode(&putBody); err != nil {
		t.Fatalf("decode put response failed: %v", err)
	}
	if putBody.ID == "" {
		t.Fatalf("expected non-empty id")
	}
	if putBody.URL != server.URL+"/v1/files/"+putBody.ID {
		t.Fatalf("unexpected url: %q", putBody.URL)
	}

	getResp, err := http.Get(server.URL + "/v1/files/" + putBody.ID)
	if err != nil {
		t.Fatalf("get request failed: %v", err)
	}
	defer getResp.Body.Close()

	if getResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", getResp.StatusCode)
	}

	deleteReq, err := http.NewRequest(http.MethodDelete, server.URL+"/v1/files/"+putBody.ID, nil)
	if err != nil {
		t.Fatalf("create delete request failed: %v", err)
	}
	deleteResp, err := http.DefaultClient.Do(deleteReq)
	if err != nil {
		t.Fatalf("delete request failed: %v", err)
	}
	defer deleteResp.Body.Close()

	if deleteResp.StatusCode != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", deleteResp.StatusCode)
	}

	getAfterDeleteResp, err := http.Get(server.URL + "/v1/files/" + putBody.ID)
	if err != nil {
		t.Fatalf("second get request failed: %v", err)
	}
	defer getAfterDeleteResp.Body.Close()

	if getAfterDeleteResp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404 after delete, got %d", getAfterDeleteResp.StatusCode)
	}
}

func TestPutTooLarge(t *testing.T) {
	temp := t.TempDir()
	metadataStore, err := sqlite.NewStore(filepath.Join(temp, "lesac.db"))
	if err != nil {
		t.Fatalf("new sqlite store failed: %v", err)
	}
	defer metadataStore.Close()

	blobStore, err := filesystem.NewStore(filepath.Join(temp, "files"))
	if err != nil {
		t.Fatalf("new filesystem store failed: %v", err)
	}

	service := usecase.NewService(metadataStore, blobStore, nil, nil)
	server := httptest.NewServer(NewHandler(service, 4))
	defer server.Close()

	resp, err := http.DefaultClient.Do(
		mustRequest(http.MethodPut, server.URL+"/v1/files", []byte("hello")),
	)
	if err != nil {
		t.Fatalf("put request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("expected 413, got %d", resp.StatusCode)
	}
}

func TestBatchPutSuccess(t *testing.T) {
	temp := t.TempDir()
	metadataStore, err := sqlite.NewStore(filepath.Join(temp, "lesac.db"))
	if err != nil {
		t.Fatalf("new sqlite store failed: %v", err)
	}
	defer metadataStore.Close()

	blobStore, err := filesystem.NewStore(filepath.Join(temp, "files"))
	if err != nil {
		t.Fatalf("new filesystem store failed: %v", err)
	}

	service := usecase.NewService(metadataStore, blobStore, nil, nil)
	server := httptest.NewServer(NewHandler(service, 1024*1024))
	defer server.Close()

	req := mustMultipartRequest(t, server.URL+"/v1/files/batch", []multipartItem{
		{name: "a.txt", field: "files", content: []byte("one")},
		{name: "b.txt", field: "files", content: []byte("two")},
	})
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("batch request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("expected 201, got %d", resp.StatusCode)
	}

	var payload struct {
		Results []struct {
			Index int    `json:"index"`
			ID    string `json:"id"`
			URL   string `json:"url"`
		} `json:"results"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatalf("decode response failed: %v", err)
	}
	if len(payload.Results) != 2 {
		t.Fatalf("expected two results, got %d", len(payload.Results))
	}
	if payload.Results[0].ID == "" || payload.Results[1].ID == "" {
		t.Fatalf("expected non-empty ids in batch response")
	}
	if payload.Results[0].URL != server.URL+"/v1/files/"+payload.Results[0].ID {
		t.Fatalf("unexpected first result url: %q", payload.Results[0].URL)
	}
	if payload.Results[1].URL != server.URL+"/v1/files/"+payload.Results[1].ID {
		t.Fatalf("unexpected second result url: %q", payload.Results[1].URL)
	}
}

func TestBatchPutPartialFailure(t *testing.T) {
	metadata := &inMemoryMetadataStore{items: map[domain.FileID]domain.FileMeta{}}
	blobs := &flakyBlobStore{
		blobs:      map[string][]byte{},
		failOnCall: 2,
	}
	service := usecase.NewService(metadata, blobs, nil, nil)
	server := httptest.NewServer(NewHandler(service, 1024*1024))
	defer server.Close()

	req := mustMultipartRequest(t, server.URL+"/v1/files/batch", []multipartItem{
		{name: "a.txt", field: "files", content: []byte("one")},
		{name: "b.txt", field: "files", content: []byte("two")},
	})
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("batch request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusMultiStatus {
		t.Fatalf("expected 207, got %d", resp.StatusCode)
	}

	var payload map[string][]map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatalf("decode response failed: %v", err)
	}
	results := payload["results"]
	if len(results) != 2 {
		t.Fatalf("expected two results, got %d", len(results))
	}
	if _, ok := results[0]["id"]; !ok {
		t.Fatalf("expected first result to include id")
	}
	if _, ok := results[0]["url"]; !ok {
		t.Fatalf("expected first result to include url")
	}
	if _, ok := results[1]["error"]; !ok {
		t.Fatalf("expected second result to include error")
	}
}

func TestBatchPutRejectsInvalidLifetime(t *testing.T) {
	temp := t.TempDir()
	metadataStore, err := sqlite.NewStore(filepath.Join(temp, "lesac.db"))
	if err != nil {
		t.Fatalf("new sqlite store failed: %v", err)
	}
	defer metadataStore.Close()

	blobStore, err := filesystem.NewStore(filepath.Join(temp, "files"))
	if err != nil {
		t.Fatalf("new filesystem store failed: %v", err)
	}

	service := usecase.NewService(metadataStore, blobStore, nil, nil)
	server := httptest.NewServer(NewHandler(service, 1024*1024))
	defer server.Close()

	req := mustMultipartRequest(t, server.URL+"/v1/files/batch?lifetime=0", []multipartItem{
		{name: "a.txt", field: "files", content: []byte("one")},
	})
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("batch request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", resp.StatusCode)
	}
}

func TestBatchPutRejectsNoFiles(t *testing.T) {
	temp := t.TempDir()
	metadataStore, err := sqlite.NewStore(filepath.Join(temp, "lesac.db"))
	if err != nil {
		t.Fatalf("new sqlite store failed: %v", err)
	}
	defer metadataStore.Close()

	blobStore, err := filesystem.NewStore(filepath.Join(temp, "files"))
	if err != nil {
		t.Fatalf("new filesystem store failed: %v", err)
	}

	service := usecase.NewService(metadataStore, blobStore, nil, nil)
	server := httptest.NewServer(NewHandler(service, 1024*1024))
	defer server.Close()

	req := mustMultipartRequest(t, server.URL+"/v1/files/batch", []multipartItem{
		{name: "a.txt", field: "ignored", content: []byte("one")},
	})
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("batch request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", resp.StatusCode)
	}
}

func TestBatchPutTooLarge(t *testing.T) {
	temp := t.TempDir()
	metadataStore, err := sqlite.NewStore(filepath.Join(temp, "lesac.db"))
	if err != nil {
		t.Fatalf("new sqlite store failed: %v", err)
	}
	defer metadataStore.Close()

	blobStore, err := filesystem.NewStore(filepath.Join(temp, "files"))
	if err != nil {
		t.Fatalf("new filesystem store failed: %v", err)
	}

	service := usecase.NewService(metadataStore, blobStore, nil, nil)
	server := httptest.NewServer(NewHandler(service, 32))
	defer server.Close()

	req := mustMultipartRequest(t, server.URL+"/v1/files/batch", []multipartItem{
		{name: "a.txt", field: "files", content: bytes.Repeat([]byte("x"), 128)},
	})
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("batch request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("expected 413, got %d", resp.StatusCode)
	}
}

func mustRequest(method, url string, body []byte) *http.Request {
	req, err := http.NewRequest(method, url, bytes.NewReader(body))
	if err != nil {
		panic(err)
	}
	return req
}

type multipartItem struct {
	field   string
	name    string
	content []byte
}

func mustMultipartRequest(t *testing.T, url string, items []multipartItem) *http.Request {
	t.Helper()

	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	for _, item := range items {
		part, err := writer.CreateFormFile(item.field, item.name)
		if err != nil {
			t.Fatalf("create multipart part failed: %v", err)
		}
		if _, err := part.Write(item.content); err != nil {
			t.Fatalf("write multipart part failed: %v", err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close multipart writer failed: %v", err)
	}

	req, err := http.NewRequest(http.MethodPost, url, body)
	if err != nil {
		t.Fatalf("create multipart request failed: %v", err)
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	return req
}

type inMemoryMetadataStore struct {
	items map[domain.FileID]domain.FileMeta
}

var _ ports.MetadataStore = (*inMemoryMetadataStore)(nil)

func (s *inMemoryMetadataStore) Save(_ context.Context, meta domain.FileMeta) error {
	s.items[meta.ID] = meta
	return nil
}

func (s *inMemoryMetadataStore) Get(_ context.Context, id domain.FileID) (domain.FileMeta, error) {
	meta, ok := s.items[id]
	if !ok {
		return domain.FileMeta{}, domain.ErrNotFound
	}
	return meta, nil
}

func (s *inMemoryMetadataStore) Delete(_ context.Context, id domain.FileID) error {
	if _, ok := s.items[id]; !ok {
		return domain.ErrNotFound
	}
	delete(s.items, id)
	return nil
}

func (s *inMemoryMetadataStore) ListExpired(
	_ context.Context,
	before time.Time,
	_ int,
) ([]domain.FileMeta, error) {
	items := make([]domain.FileMeta, 0)
	for _, meta := range s.items {
		if meta.ExpiresAt != nil && !meta.ExpiresAt.After(before) {
			items = append(items, meta)
		}
	}
	return items, nil
}

type flakyBlobStore struct {
	blobs      map[string][]byte
	putCalls   int
	failOnCall int
}

var _ ports.BlobStore = (*flakyBlobStore)(nil)

func (s *flakyBlobStore) Put(_ context.Context, key string, body io.Reader) error {
	s.putCalls++
	if s.failOnCall > 0 && s.putCalls == s.failOnCall {
		return errors.New("simulated put failure")
	}
	data, err := io.ReadAll(body)
	if err != nil {
		return err
	}
	s.blobs[key] = data
	return nil
}

func (s *flakyBlobStore) Get(_ context.Context, key string) (io.ReadCloser, error) {
	data, ok := s.blobs[key]
	if !ok {
		return nil, domain.ErrNotFound
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}

func (s *flakyBlobStore) Delete(_ context.Context, key string) error {
	if _, ok := s.blobs[key]; !ok {
		return domain.ErrNotFound
	}
	delete(s.blobs, key)
	return nil
}
