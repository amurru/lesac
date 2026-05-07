package httpadapter

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"path/filepath"
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

	var body io.Reader = r.Body
	if h.maxUploadBytes > 0 {
		body = http.MaxBytesReader(w, r.Body, h.maxUploadBytes)
	}

	meta, body, err := extractPutUploadMeta(r.Header.Get("Content-Type"), body)
	if err != nil {
		var tooLarge *http.MaxBytesError
		switch {
		case errors.As(err, &tooLarge):
			writeJSONError(w, http.StatusRequestEntityTooLarge, "upload too large")
		default:
			writeJSONError(w, http.StatusInternalServerError, "failed to read file")
		}
		return
	}
	id, err := h.service.Put(r.Context(), body, lifetime, meta)
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
		"id":        id.String(),
		"url":       fileURL(r, id),
		"mimetype":  meta.MIMEType,
		"extension": meta.Extension,
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
			item["mimetype"] = result.Metadata.MIMEType
			item["extension"] = result.Metadata.Extension
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
	body, meta, err := h.service.Get(r.Context(), id)
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

	contentType := meta.MIMEType
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	w.Header().Set("Content-Type", contentType)
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

		itemMeta := extractUploadMeta(part.Header.Get("Content-Type"), part.FileName())
		data, err := io.ReadAll(part)
		_ = part.Close()
		if err != nil {
			return nil, err
		}
		items = append(items, usecase.PutBatchItem{
			Body:     bytes.Clone(data),
			Metadata: itemMeta,
		})
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

func extractUploadMeta(contentType, filename string) usecase.PutFileMeta {
	mimeType := normalizeMIMEType(contentType)
	extension := normalizeExtension(filepath.Ext(filename))
	if extension == "" && mimeType != "" {
		extension = extensionFromMIMEType(mimeType)
	}
	return usecase.PutFileMeta{
		MIMEType:  mimeType,
		Extension: extension,
	}
}

func extractPutUploadMeta(contentType string, body io.Reader) (usecase.PutFileMeta, io.Reader, error) {
	meta := extractUploadMeta(contentType, "")
	if !shouldSniffMIMEType(meta.MIMEType) {
		return meta, body, nil
	}

	const sniffBytes = 512
	head := make([]byte, sniffBytes)
	n, err := io.ReadFull(body, head)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		return usecase.PutFileMeta{}, nil, err
	}
	head = head[:n]

	sniffed := normalizeMIMEType(http.DetectContentType(head))
	if sniffed != "" {
		meta.MIMEType = sniffed
		if meta.Extension == "" {
			meta.Extension = extensionFromMIMEType(sniffed)
		}
	}
	return meta, io.MultiReader(bytes.NewReader(head), body), nil
}

func shouldSniffMIMEType(mimeType string) bool {
	switch mimeType {
	case "", "application/octet-stream", "application/x-www-form-urlencoded":
		return true
	default:
		return false
	}
}

func normalizeMIMEType(value string) string {
	if value == "" {
		return ""
	}
	mediaType, _, err := mime.ParseMediaType(value)
	if err != nil {
		return ""
	}
	return strings.ToLower(mediaType)
}

func extensionFromMIMEType(mimeType string) string {
	extensions, err := mime.ExtensionsByType(mimeType)
	if err != nil || len(extensions) == 0 {
		return ""
	}
	preferred := preferredExtensionForMIMEType(mimeType)
	if preferred != "" {
		for _, candidate := range extensions {
			if normalizeExtension(candidate) == preferred {
				return preferred
			}
		}
	}
	return normalizeExtension(extensions[0])
}

func preferredExtensionForMIMEType(mimeType string) string {
	switch mimeType {
	case "text/plain":
		return "txt"
	case "image/jpeg":
		return "jpg"
	default:
		return ""
	}
}

func normalizeExtension(value string) string {
	normalized := strings.TrimSpace(strings.ToLower(strings.TrimPrefix(value, ".")))
	return normalized
}
