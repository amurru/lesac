#!/usr/bin/env python3
"""Stream files listed in a CSV into lesac without saving files locally."""

from __future__ import annotations

import argparse
import csv
import http.client
import json
import ssl
import sys
from dataclasses import dataclass
from typing import Dict
from urllib.error import HTTPError, URLError
from urllib.parse import urlencode, urlsplit
from urllib.request import Request, urlopen


AUTO_CSV_ENCODINGS = ("utf-8-sig", "utf-8", "cp1252", "latin-1")


@dataclass
class LesacEndpoint:
    scheme: str
    host: str
    port: int
    path: str


def parse_headers(raw_headers: list[str]) -> Dict[str, str]:
    headers: Dict[str, str] = {}
    for raw in raw_headers:
        if ":" not in raw:
            raise ValueError(f"invalid header format: {raw!r} (expected 'Name: Value')")
        name, value = raw.split(":", 1)
        headers[name.strip()] = value.strip()
    return headers


def build_endpoint(base_url: str, lifetime: int | None) -> LesacEndpoint:
    parsed = urlsplit(base_url)
    if parsed.scheme not in ("http", "https"):
        raise ValueError(f"unsupported lesac scheme: {parsed.scheme!r}")
    if not parsed.hostname:
        raise ValueError(f"invalid lesac base URL: {base_url!r}")

    port = parsed.port
    if not port:
        port = 443 if parsed.scheme == "https" else 80

    root_path = parsed.path.rstrip("/")
    upload_path = f"{root_path}/v1/files" if root_path else "/v1/files"
    if lifetime is not None:
        upload_path = f"{upload_path}?{urlencode({'lifetime': lifetime})}"

    return LesacEndpoint(
        scheme=parsed.scheme,
        host=parsed.hostname,
        port=port,
        path=upload_path,
    )


def resolve_csv_encoding(csv_path: str, csv_encoding: str) -> str:
    if csv_encoding.lower() != "auto":
        return csv_encoding

    for encoding in AUTO_CSV_ENCODINGS:
        try:
            with open(csv_path, newline="", encoding=encoding) as probe:
                probe.read()
            return encoding
        except UnicodeDecodeError:
            continue

    raise ValueError(
        "failed to decode CSV with auto-detect; specify --csv-encoding explicitly"
    )


def upload_stream(
    source_url: str,
    mimetype: str,
    endpoint: LesacEndpoint,
    source_headers: Dict[str, str],
    lesac_headers: Dict[str, str],
    timeout: float,
    tls_context: ssl.SSLContext | None,
) -> tuple[int, str]:
    req = Request(source_url, headers=source_headers, method="GET")
    with urlopen(req, timeout=timeout, context=tls_context) as source_response:
        conn_cls = (
            http.client.HTTPSConnection
            if endpoint.scheme == "https"
            else http.client.HTTPConnection
        )
        if endpoint.scheme == "https":
            conn = conn_cls(
                endpoint.host, endpoint.port, timeout=timeout, context=tls_context
            )
        else:
            conn = conn_cls(endpoint.host, endpoint.port, timeout=timeout)
        try:
            headers = {"Content-Type": mimetype or "application/octet-stream"}
            headers.update(lesac_headers)
            conn.request(
                "PUT",
                endpoint.path,
                body=source_response,
                headers=headers,
                encode_chunked=True,
            )
            response = conn.getresponse()
            payload = response.read().decode("utf-8", errors="replace")
            return response.status, payload
        finally:
            conn.close()


def main() -> int:
    parser = argparse.ArgumentParser(
        description="Stream source URLs from CSV directly into lesac."
    )
    parser.add_argument("--csv", default="test.csv", help="Path to input CSV file")
    parser.add_argument(
        "--csv-encoding",
        default="auto",
        help="CSV input encoding (default: auto; tries utf-8-sig, utf-8, cp1252, latin-1)",
    )
    parser.add_argument(
        "--output",
        default="lesac_uploaded.csv",
        help="Path to output CSV with lesac URLs and status columns",
    )
    parser.add_argument(
        "--lesac-base",
        required=True,
        help="lesac base URL, e.g. http://localhost:8080",
    )
    parser.add_argument("--url-column", default="URL", help="CSV column with source URL")
    parser.add_argument(
        "--mime-column",
        default="MIME_TYPE",
        help="CSV column with MIME type (fallback: application/octet-stream)",
    )
    parser.add_argument(
        "--id-column",
        default="ID",
        help="CSV column to include as source_id in output logs",
    )
    parser.add_argument(
        "--lifetime",
        type=int,
        default=None,
        help="Optional file lifetime in seconds for lesac upload",
    )
    parser.add_argument(
        "--timeout",
        type=float,
        default=60.0,
        help="HTTP timeout in seconds for source and destination requests",
    )
    parser.add_argument(
        "--source-header",
        action="append",
        default=[],
        metavar="HEADER",
        help="Header for source GET request, format: 'Name: Value' (repeatable)",
    )
    parser.add_argument(
        "--lesac-header",
        action="append",
        default=[],
        metavar="HEADER",
        help="Header for lesac PUT request, format: 'Name: Value' (repeatable)",
    )
    parser.add_argument(
        "--insecure-tls",
        action="store_true",
        help="Disable TLS certificate verification for both source and lesac HTTPS connections",
    )
    args = parser.parse_args()

    if args.lifetime is not None and args.lifetime <= 0:
        print("--lifetime must be a positive integer", file=sys.stderr)
        return 2

    try:
        source_headers = parse_headers(args.source_header)
        lesac_headers = parse_headers(args.lesac_header)
        endpoint = build_endpoint(args.lesac_base, args.lifetime)
        csv_encoding = resolve_csv_encoding(args.csv, args.csv_encoding)
    except ValueError as err:
        print(str(err), file=sys.stderr)
        return 2

    tls_context: ssl.SSLContext | None = None
    if args.insecure_tls:
        tls_context = ssl.create_default_context()
        tls_context.check_hostname = False
        tls_context.verify_mode = ssl.CERT_NONE

    total = 0
    success = 0
    result_columns = [
        "LESAC_STATUS",
        "LESAC_ID",
        "LESAC_URL",
        "LESAC_MIMETYPE",
        "LESAC_EXTENSION",
        "LESAC_ERROR",
    ]

    with open(args.csv, newline="", encoding=csv_encoding) as csvfile, open(
        args.output, "w", newline="", encoding="utf-8"
    ) as outfile:
        reader = csv.DictReader(csvfile)
        if not reader.fieldnames:
            print("input CSV has no header row", file=sys.stderr)
            return 2

        output_fieldnames = list(reader.fieldnames)
        for column in result_columns:
            if column not in output_fieldnames:
                output_fieldnames.append(column)

        writer = csv.DictWriter(outfile, fieldnames=output_fieldnames)
        writer.writeheader()

        for row_index, row in enumerate(reader, start=1):
            total += 1
            source_url = (row.get(args.url_column) or "").strip()
            mimetype = (row.get(args.mime_column) or "").strip()
            out_row = dict(row)
            out_row["LESAC_STATUS"] = "ERROR"
            out_row["LESAC_ID"] = ""
            out_row["LESAC_URL"] = ""
            out_row["LESAC_MIMETYPE"] = ""
            out_row["LESAC_EXTENSION"] = ""
            out_row["LESAC_ERROR"] = ""

            if not source_url:
                out_row["LESAC_ERROR"] = f"missing URL in column {args.url_column!r}"
                writer.writerow(out_row)
                continue

            try:
                status_code, body = upload_stream(
                    source_url=source_url,
                    mimetype=mimetype,
                    endpoint=endpoint,
                    source_headers=source_headers,
                    lesac_headers=lesac_headers,
                    timeout=args.timeout,
                    tls_context=tls_context,
                )
                if status_code == 201:
                    success += 1
                    out_row["LESAC_STATUS"] = "UPLOADED"
                else:
                    out_row["LESAC_STATUS"] = f"HTTP_{status_code}"

                try:
                    parsed_body = json.loads(body)
                except json.JSONDecodeError:
                    parsed_body = {}

                if isinstance(parsed_body, dict):
                    out_row["LESAC_ID"] = str(parsed_body.get("id", ""))
                    out_row["LESAC_URL"] = str(parsed_body.get("url", ""))
                    out_row["LESAC_MIMETYPE"] = str(parsed_body.get("mimetype", ""))
                    out_row["LESAC_EXTENSION"] = str(parsed_body.get("extension", ""))
                    if status_code != 201:
                        out_row["LESAC_ERROR"] = str(parsed_body.get("error", body))
                elif status_code != 201:
                    out_row["LESAC_ERROR"] = body
            except (HTTPError, URLError, TimeoutError, OSError, http.client.HTTPException) as err:
                out_row["LESAC_ERROR"] = str(err)

            writer.writerow(out_row)

    print(
        json.dumps(
            {
                "summary": {
                    "total": total,
                    "success": success,
                    "failed": total - success,
                    "output": args.output,
                    "csv_encoding": csv_encoding,
                }
            }
        ),
        file=sys.stderr,
    )
    return 0 if success == total else 1


if __name__ == "__main__":
    raise SystemExit(main())
