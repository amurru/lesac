# le sac

`le sac` is a small file-store server in Go.

## Features

- HTTP adapter (`PUT`, `GET`, `DELETE`) with a transport-agnostic core service.
- Pluggable storage architecture (filesystem implemented now).
- SQLite metadata store.
- Optional file lifetime (TTL) in seconds.
- TOML configuration.
- Service-friendly runtime (foreground process + graceful shutdown on signals).

## API (v1)

### `PUT /v1/files`

Stores the raw request body as a file.

- Query parameter: `lifetime` (optional, in seconds).
- `lifetime=0` is invalid and returns `400`.
- `mimetype` is derived from `Content-Type`; when missing/generic (for example `curl --data-binary` default), the server sniffs bytes to detect type.
- Success response:

```json
{
  "id": "<unique-id>",
  "url": "http://<host>/v1/files/<unique-id>",
  "mimetype": "text/plain",
  "extension": "txt"
}
```

Status: `201 Created`

### `POST /v1/files/batch`

Stores multiple files in one request.

- Content type: `multipart/form-data`
- Repeated file field name: `files`
- Query parameter: `lifetime` (optional, in seconds, applied to all files)

Response body:

```json
{
  "results": [
    {
      "index": 0,
      "id": "<unique-id>",
      "url": "http://<host>/v1/files/<unique-id>",
      "mimetype": "image/png",
      "extension": "png"
    },
    { "index": 1, "error": "failed to store file" }
  ]
}
```

- `201 Created`: all files stored
- `207 Multi-Status`: partial success
- `500 Internal Server Error`: all files failed
- `400 Bad Request`: invalid lifetime or invalid/missing multipart file parts

### `GET /v1/files/{id}`

Retrieves raw file bytes.

- Status: `200 OK` on success.
- Status: `404 Not Found` when file is missing or expired.
- Response `Content-Type` is the stored file `mimetype` when available, otherwise `application/octet-stream`.

### `DELETE /v1/files/{id}`

Deletes a file.

- Idempotent: missing IDs still return success.
- Status: `204 No Content`

## Running

```bash
go run ./cmd/lesac -config ./config.toml
```

## Configuration

See [`config.toml`](./config.toml). Key defaults:

- `uploads.max_upload_size = 67108864` (64 MiB)
- `storage.driver = "filesystem"`

## Example

Upload:

```bash
curl -X PUT --data-binary @myfile.bin "http://localhost:8080/v1/files?lifetime=300"
```

Batch upload:

```bash
curl -X POST "http://localhost:8080/v1/files/batch?lifetime=300" \
  -F "files=@a.bin" \
  -F "files=@b.bin"
```

Download:

```bash
curl -o myfile.bin "http://localhost:8080/v1/files/<id>"
```

Delete:

```bash
curl -X DELETE "http://localhost:8080/v1/files/<id>"
```
