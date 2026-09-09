---
name: sink
description: >
  Use the sink HTTP file-storage service to upload files (optionally unpacking
  zip/tar archives into a subtree), list the storage tree, view or download a
  file, and download a subtree as zip or tar. Use when uploading, retrieving,
  unpacking, browsing, or archiving files in sink storage.
---

# sink storage skill

Base URL: `{{BASE_URL}}`

All stored paths are relative to the server's `storage/` directory. Never send `..`, absolute paths, Windows drive prefixes, or a `storage/` prefix. Use `/` as the separator. The empty path is the storage root.

Treat this document as the API contract. Prefer the JSON `/api/*` endpoints over HTML.

## Rules

- Destination `path` is required for upload. It is relative to `storage/`.
- Uploading a regular file writes that file at `path` (parents are created).
- Uploading with `unpack` extracts the archive **into** `path` as a directory (the subtree root). Archive entries cannot escape `path`.
- Overwriting existing files is allowed. You cannot store a file at a path that is already a directory, or unpack into a path that is already a file.
- Do not unpack a lone gzip/bzip2/xz stream; only zip and tar (optionally compressed).
- Symlinks and special files in archives are skipped. Symlinks in storage are not served.

## Upload a file

`POST {{BASE_URL}}/api/upload`

Two equivalent methods. Both return JSON.

### 1. Raw body (preferred for a single file)

```
POST /api/upload?path=docs/notes.txt
Content-Type: application/octet-stream

<file bytes>
```

```bash
curl -sS -X POST "{{BASE_URL}}/api/upload?path=docs/notes.txt" \
  --data-binary @notes.txt
```

### 2. Multipart form (required for browser-style uploads; also fine from curl)

Fields:

| Field | Required | Meaning |
| --- | --- | --- |
| `path` | yes | Destination relative path |
| `file` | yes | File bytes (field name **must** be `file`) |
| `unpack` | no | See unpack values below |

`path` and `unpack` may also be query parameters. Form fields override query parameters.

```bash
curl -sS -X POST "{{BASE_URL}}/api/upload" \
  -F "path=docs/notes.txt" \
  -F "file=@notes.txt"
```

### Unpack an archive into a subtree

Set `unpack` and set `path` to the directory that should become the subtree root. Example: archive entries `a.txt` and `b/c.txt` with `path=vendor/lib` become `vendor/lib/a.txt` and `vendor/lib/b/c.txt`.

| `unpack` value | Behavior |
| --- | --- |
| omitted, `false`, `0`, `no`, `none` | Store the upload as a single file at `path` |
| `true`, `1`, `yes`, `auto` | Sniff zip/tar/tar.gz/tar.bz2/tar.xz and extract into `path` |
| `zip` | Extract zip into `path` |
| `tar` | Extract tar into `path` |
| `tar.gz`, `tgz` | Extract gzip-compressed tar into `path` |
| `tar.bz2`, `tbz2`, `tbz` | Extract bzip2-compressed tar into `path` |
| `tar.xz`, `txz` | Extract xz-compressed tar into `path` |

`path` must not end with `/` unless unpacking (a trailing slash is only valid as a directory target).

```bash
curl -sS -X POST "{{BASE_URL}}/api/upload?path=vendor/lib&unpack=auto" \
  --data-binary @lib.tar.gz
```

```bash
curl -sS -X POST "{{BASE_URL}}/api/upload" \
  -F "path=vendor/lib" \
  -F "unpack=zip" \
  -F "file=@lib.zip"
```

### Upload response

```json
{
  "ok": true,
  "path": "vendor/lib",
  "stored": "directory",
  "unpacked": true,
  "format": "tar.gz",
  "bytes": 4096,
  "files": 12
}
```

`stored` is `"file"` or `"directory"`. `bytes` is bytes written to disk. `files` is the number of regular files written.

## List paths

`GET {{BASE_URL}}/api/tree/{path}`

`GET {{BASE_URL}}/api/tree` lists the storage root.

Query:

| Param | Meaning |
| --- | --- |
| `recursive=1` | Include all descendants (flat list). Default is immediate children only. |

```bash
curl -sS "{{BASE_URL}}/api/tree"
curl -sS "{{BASE_URL}}/api/tree/docs"
curl -sS "{{BASE_URL}}/api/tree?recursive=1"
```

Response:

```json
{
  "ok": true,
  "path": "docs",
  "type": "directory",
  "size": 0,
  "mod_time": "2026-09-09T12:00:00Z",
  "entries": [
    {
      "name": "notes.txt",
      "path": "docs/notes.txt",
      "type": "file",
      "size": 123,
      "mod_time": "2026-09-09T12:00:00Z"
    }
  ],
  "truncated": false
}
```

`type` is `file` or `directory`. If `{path}` names a file, `entries` is omitted and `type` is `file`. If the listing hits the entry cap, `truncated` is `true`.

## Download a file

`GET {{BASE_URL}}/api/file/{path}`

Returns the raw bytes. Directories are not served here (use `/api/tree` or `/api/archive`).

```bash
curl -sS -o notes.txt "{{BASE_URL}}/api/file/docs/notes.txt"
```

Add `?download=1` to force `Content-Disposition: attachment`.

## Download a subtree as an archive

`GET {{BASE_URL}}/api/archive/{path}`

`GET {{BASE_URL}}/api/archive` archives the whole storage root.

Query:

| Param | Values | Default |
| --- | --- | --- |
| `format` | `zip`, `tar`, `tar.gz` | `zip` |

A directory is packed with that directory as the top-level folder (root listings have no extra prefix). A file is packed as a single-entry archive.

```bash
curl -sS -o docs.zip "{{BASE_URL}}/api/archive/docs?format=zip"
curl -sS -o all.tar.gz "{{BASE_URL}}/api/archive?format=tar.gz"
```

## HTML UI (humans)

Not required for agents. Linked here so you do not confuse them with the API.

| URL | Page |
| --- | --- |
| `GET {{BASE_URL}}/` | Directory browser at storage root |
| `GET {{BASE_URL}}/browse/{path}` | Directory browser |
| `GET {{BASE_URL}}/view/{path}` | View one file (Markdown is previewed; add `?mode=raw` for the source) |
| `POST {{BASE_URL}}/upload` | HTML form post (redirects; do not use) |

## Errors

JSON body: `{"ok": false, "error": "<message>"}`.

| Status | When |
| --- | --- |
| 400 | Bad path, missing file, invalid `unpack`, non-archive when unpacking |
| 404 | Path does not exist |
| 409 | File/directory type conflict at the destination |
| 413 | Upload or extracted size/file-count exceeds server limits |
| 500 | Unexpected server error |

## Procedure

1. List with `GET /api/tree` (add `?recursive=1` if you need the full picture).
2. Upload with `POST /api/upload`. Use `unpack=auto` only when the body is a zip or tar archive you want extracted.
3. Verify with `GET /api/tree/{parent}` or `GET /api/file/{path}`.
4. To take a subtree away, `GET /api/archive/{path}?format=tar.gz`.

Path examples (all legal): `readme.md`, `pkg/mod/github.com/foo@v1.0.0`, `datasets/2026-09-09/run.bin`.
