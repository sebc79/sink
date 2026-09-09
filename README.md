# sink

A small Go HTTP service that stores files under a local `storage/` directory so **agents can hand humans something openable** — a view URL — instead of a path on a machine the human cannot see.

## Why this exists

AI agents often write useful files (notes, plans, checklists, logs) on *their* computer. Chat is a bad delivery surface for that:

- Pasting the whole file blows context and is painful to reread.
- A local path (`/home/box/...`) only works if you share the agent's filesystem.
- Raw cloud URLs in model context leak into logs and still need auth/expiry design.

So the handoff should be a **delivery contract**: short chat summary + a link the human can open in a browser. That is what sink is.

Other products solve pieces of this with artifact panes, PR attachments, Slack uploads, or signed object URLs. sink is the same idea as a tiny self-hosted service: upload from the agent, browse/view from the human, skill document for the agent API.

## Use with Grok Bot (and similar agents)

1. Run sink where the agent can reach it (same Tailscale network is enough). Point `-addr` / `-storage` as needed.
2. Tell the agent the house rule (or paste it into its instructions):

   > When you hand me a file, upload it to sink and send the **view URL**, not a box path.  
   > Skill contract: `http://<host>:8080/skill`  
   > Example view URL: `http://<host>:8080/view/projects/cv/notes/example.md`  
   > Keep durable tree paths for *other bots* / your own filing — sink does not replace that.

3. Agent flow: `GET /skill` → `POST /api/upload?path=...` with the file bytes → reply with `http://<host>:8080/view/<path>`.
4. Human opens the view URL (markdown renders; raw still available).

No auth in the default build — treat LAN/Tailscale reachability as your perimeter, or put it behind your own reverse proxy if you need more.

## API (short)

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
