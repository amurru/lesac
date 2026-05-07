package httpadapter

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strconv"
	"strings"

	"github.com/amurru/lesac/internal/domain"
	"github.com/amurru/lesac/internal/usecase"
)

// Handler serves the HTTP API endpoints for file operations.
type Handler struct {
	service        *usecase.Service
	maxUploadBytes int64
}

// NewHandler builds the HTTP API handler for file upload, download, and deletion.
func NewHandler(service *usecase.Service, maxUploadBytes int64) http.Handler {
	h := &Handler{
		service:        service,
		maxUploadBytes: maxUploadBytes,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/v1/files", h.handleCollection)
	mux.HandleFunc("/v1/files/batch", h.handleBatchPut)
	mux.HandleFunc("/v1/files/", h.handleItem)
	return mux
}

func (h *Handler) handleCollection(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut {
		w.Header().Set("Allow", http.MethodPut)
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	lifetime, err := parseLifetime(r)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid lifetime")
		return
	}

	body := r.Body
	if h.maxUploadBytes > 0 {
		body = http.MaxBytesReader(w, r.Body, h.maxUploadBytes)
	}

	id, err := h.service.Put(r.Context(), body, lifetime)
	if err != nil {
		var tooLarge *http.MaxBytesError
		switch {
		case errors.Is(err, domain.ErrInvalidLifetime):
			writeJSONError(w, http.StatusBadRequest, "invalid lifetime")
		case errors.As(err, &tooLarge):
			writeJSONError(w, http.StatusRequestEntityTooLarge, "upload too large")
		default:
			writeJSONError(w, http.StatusInternalServerError, "failed to store file")
		}
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(map[string]string{
		"id":  id.String(),
		"url": fileURL(r, id),
	})
}

func (h *Handler) handleItem(w http.ResponseWriter, r *http.Request) {
	id, err := fileIDFromPath(r.URL.Path)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid file id")
		return
	}

	switch r.Method {
	case http.MethodGet:
		h.handleGet(w, r, id)
	case http.MethodDelete:
		h.handleDelete(w, r, id)
	default:
		w.Header().Set("Allow", strings.Join([]string{http.MethodGet, http.MethodDelete}, ", "))
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (h *Handler) handleBatchPut(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	lifetime, err := parseLifetime(r)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid lifetime")
		return
	}

	body := r.Body
	if h.maxUploadBytes > 0 {
		body = http.MaxBytesReader(w, r.Body, h.maxUploadBytes)
	}
	r.Body = body

	reader, err := r.MultipartReader()
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid multipart payload")
		return
	}

	items, err := readBatchItems(reader)
	if err != nil {
		var tooLarge *http.MaxBytesError
		switch {
		case errors.As(err, &tooLarge):
			writeJSONError(w, http.StatusRequestEntityTooLarge, "upload too large")
		case errors.Is(err, domain.ErrEmptyBatch):
			writeJSONError(w, http.StatusBadRequest, "no files provided")
		default:
			writeJSONError(w, http.StatusBadRequest, "invalid multipart payload")
		}
		return
	}

	results, err := h.service.PutBatch(r.Context(), items, lifetime)
	if err != nil {
		switch {
		case errors.Is(err, domain.ErrInvalidLifetime), errors.Is(err, domain.ErrEmptyBatch):
			writeJSONError(w, http.StatusBadRequest, "invalid batch request")
		default:
			writeJSONError(w, http.StatusInternalServerError, "failed to store files")
		}
		return
	}

	responseItems := make([]map[string]any, 0, len(results))
	successCount := 0
	for _, result := range results {
		item := map[string]any{
			"index": result.Index,
		}
		if result.Err != nil {
			item["error"] = "failed to store file"
		} else {
			item["id"] = result.ID.String()
			item["url"] = fileURL(r, result.ID)
			successCount++
		}
		responseItems = append(responseItems, item)
	}

	status := http.StatusCreated
	if successCount == 0 {
		status = http.StatusInternalServerError
	} else if successCount < len(results) {
		status = http.StatusMultiStatus
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"results": responseItems})
}

func (h *Handler) handleGet(w http.ResponseWriter, r *http.Request, id domain.FileID) {
	body, err := h.service.Get(r.Context(), id)
	if err != nil {
		switch {
		case errors.Is(err, domain.ErrNotFound):
			writeJSONError(w, http.StatusNotFound, "file not found")
		default:
			writeJSONError(w, http.StatusInternalServerError, "failed to read file")
		}
		return
	}
	defer body.Close()

	w.Header().Set("Content-Type", "application/octet-stream")
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, body)
}

func (h *Handler) handleDelete(w http.ResponseWriter, r *http.Request, id domain.FileID) {
	if err := h.service.Delete(r.Context(), id); err != nil {
		switch {
		case errors.Is(err, domain.ErrNotFound):
			w.WriteHeader(http.StatusNoContent)
		default:
			writeJSONError(w, http.StatusInternalServerError, "failed to delete file")
		}
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func parseLifetime(r *http.Request) (*int64, error) {
	value := r.URL.Query().Get("lifetime")
	if value == "" {
		return nil, nil
	}

	seconds, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return nil, err
	}
	return &seconds, nil
}

func fileIDFromPath(path string) (domain.FileID, error) {
	const prefix = "/v1/files/"
	if !strings.HasPrefix(path, prefix) {
		return "", domain.ErrInvalidFileID
	}
	raw := strings.TrimPrefix(path, prefix)
	if raw == "" || strings.Contains(raw, "/") {
		return "", domain.ErrInvalidFileID
	}
	return domain.ParseFileID(raw)
}

func readBatchItems(reader *multipart.Reader) ([]usecase.PutBatchItem, error) {
	items := make([]usecase.PutBatchItem, 0)
	for {
		part, err := reader.NextPart()
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, err
		}

		if part.FormName() != "files" {
			_ = part.Close()
			continue
		}

		data, err := io.ReadAll(part)
		_ = part.Close()
		if err != nil {
			return nil, err
		}
		items = append(items, usecase.PutBatchItem{Body: bytes.Clone(data)})
	}

	if len(items) == 0 {
		return nil, domain.ErrEmptyBatch
	}

	return items, nil
}

func writeJSONError(w http.ResponseWriter, code int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": message})
}

func fileURL(r *http.Request, id domain.FileID) string {
	scheme := "http"
	if proto := r.Header.Get("X-Forwarded-Proto"); proto != "" {
		scheme = strings.Split(proto, ",")[0]
	} else if r.TLS != nil {
		scheme = "https"
	}
	return fmt.Sprintf("%s://%s/v1/files/%s", scheme, r.Host, id.String())
}
