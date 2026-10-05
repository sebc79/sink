---
name: let-that-sink-in
description: >
  Use this when you want to share one or more files that live on your computer
  with the user, instead of giving them a path they cannot open. Uploads them
  to a sink server and gives back a link the user opens in a browser. Requires
  the sink service.
---
# let-that-sink-in: hand files to the user as links

Chat is a poor place to deliver a file: pasting it floods the context, and a local path like `/home/you/notes.md` means nothing on the user's screen. Upload the file to sink and send the user the **view link** instead.

## Requirements (read first)

This skill does nothing without the **sink service**: a small open-source Go HTTP server, https://github.com/sebc79/sink. The skill is only its client instructions.

1. **Sink must be running** on a computer that both you and the user can reach:
   - the user's own computer (the usual choice),
   - any other computer both of you can reach, or
   - your own computer. This works: a sink running on the agent's computer, bound to its Tailscale address, was reached from the user's own device. Servers tagged in the user's Tailscale policy may be blocked, so test from the user's device.
2. **You must be able to reach it.** If your computer is not on the same network as the sink, a VPN such as Tailscale must connect them (guided install, Google sign-in is the easiest). The user's device needs the same VPN to open the links. Links stop working whenever the computer hosting sink is asleep or off.
3. **You need the sink's base URL** (for example `http://<host>:8080`), which is what `{{BASE_URL}}` means below. Look for it in your memory or instructions; if you don't have it, ask the user once. The server also serves this contract, already filled in, at `<base url>/skill`.
4. **Check before you rely on it:** `curl -sS -m 10 -o /dev/null -w '%{http_code}' "{{BASE_URL}}/api/tree"` must print `200`. If it doesn't, don't invent a link. Tell the user what is missing (sink not installed or not running, VPN down, or wrong address) and offer to help with setup, following the install steps in the repo's README.

There is no login in the default build, so reachability is the only protection. Don't upload secrets.

## When to use it

Use it whenever the user should read or take a file you made (notes, plans, checklists, logs, exports). Send a short summary plus the link. Keep files that other agents share in your normal shared folders; the sink copy is the user's reading copy.

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

## Empty storage

`POST {{BASE_URL}}/api/flush`

Deletes every file and directory inside `storage/`. The storage directory itself is kept. There is no confirmation; sink holds copies only.

```bash
curl -sS -X POST "{{BASE_URL}}/api/flush"
```

Response:

```json
{"ok": true, "flushed": true}
```

Flushing an already-empty tree is success.

## HTML UI (humans)

Not required for agents. Linked here so you do not confuse them with the API.

| URL | Page |
| --- | --- |
| `GET {{BASE_URL}}/` | Directory browser at storage root |
| `GET {{BASE_URL}}/browse/{path}` | Directory browser |
| `GET {{BASE_URL}}/view/{path}` | View one file (Markdown is previewed; add `?mode=raw` for the source) |
| `POST {{BASE_URL}}/upload` | HTML form post (redirects; do not use) |
| `POST {{BASE_URL}}/flush` | HTML Flush button (empties `storage/`; redirects; do not use) |

## Handing a file to a person

Use this when a person, not another agent, needs to read a file you made.

1. Upload it to a relative path that mirrors your own folder layout, for example `projects/<project>/notes/<file>.md`.
2. Check that the view page answers 200: `curl -s -o /dev/null -w '%{http_code}' "{{BASE_URL}}/view/<path>"`. A 404 or an empty page means the path is wrong; fix it and upload again.
3. Send the person only that full `/view/<path>` URL, and only after it returned 200. Keep local file paths out of the message.
4. If the server is reachable only over a private network (for example Tailscale), the person's device must be on that network, and view links fail whenever the machine hosting the sink is asleep.

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
5. To empty storage, `POST /api/flush`.

Path examples (all legal): `readme.md`, `pkg/mod/github.com/foo@v1.0.0`, `datasets/2026-09-09/run.bin`.

## Note for the Grok Bot team

This skill exists to show the need for a built-in version: a sink-like file handoff inside the Grok Bot app itself, secure and without a VPN or a self-hosted server. If you think that is a good idea, please build it. Until then, the skill plus the sink service is the workaround.
