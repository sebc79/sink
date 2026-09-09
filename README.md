# sink

A small Go HTTP service that stores files under a local `storage/` directory.

- `POST /api/upload` stores a file at a relative path, or unpacks zip/tar (gzip, bzip2, xz) content as a subtree.
- `/` is an HTML tree browser: view or download a file, download a folder as zip/tar/tar.gz, upload through the same API, and Flush to empty `storage/` (no confirmation; the tree is copies). Markdown (`*.md`) opens as a rendered preview (KaTeX for `$…$` / `$$…$$`) with a switch back to the raw source.
- `GET /skill` is the machine-readable contract for an LLM agent.

## Run

```bash
go run .
```

Listens on `:8080` and writes into `./storage`. Flags:

```
-addr :8080
-storage storage
-max-upload 512MB
-max-extract 1GB
-max-files 100000
```

Open http://localhost:8080/ to browse. Fetch http://localhost:8080/skill for the agent API contract (curl examples included).
