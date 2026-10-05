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

The **`let-that-sink-in`** skill teaches an agent when and how to use sink: whenever it wants to share a file from its computer with you, it sends you a link instead of a path, linking files in a synced folder directly and uploading anything else. Get it from [`skills/let-that-sink-in/SKILL.md`](skills/let-that-sink-in/SKILL.md) in this repository; [`skill.md`](skill.md) is the full API the server serves at `/skill` (a marketplace listing will be linked here once it is published). The skill is only the client instructions. It needs this service running, reachable by both the agent and you (see below).

### Requirements

- **Run sink** on a computer both you and the agent can reach: your own computer, another computer you both reach, or the agent's computer (tested: a sink on a Grok Bot computer, bound to its Tailscale address, was reached from the user's own device; servers tagged in your Tailscale policy may be blocked).
- **A VPN such as [Tailscale](https://tailscale.com)** if the agent's computer is not on your network: install it on both machines (Google sign-in is the easiest) so the agent can upload and your device can open the links. Links stop working while the machine hosting sink is asleep or off.
- **Optional, for links without upload: a synced folder** such as an ArborSync checkout of the agent's files on the sink host, passed with `-tree` (see below).
- **Give the agent the base URL** (for example `http://<host>:8080`). The skill asks for it if it doesn't have it.

### Set up

1. Run sink where the agent can reach it (`-addr` / `-storage` as needed).
2. Install the `let-that-sink-in` skill, or paste this house rule into the agent's instructions:

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
-tree
-peer-socket
-sender-slave grok-bot-box
-hold-timeout 30s
-index-rescan 45s
-max-upload 512MB
-max-extract 1GB
-max-files 100000
```

`-tree` is an optional ArborSync checkout, for example `/home/box/knowledge`. `/view` and `/api/file` resolve a path in this order: exact tree path, exact storage path, then a unique basename or trailing-segment match in the tree. `/`, `/browse`, `/api/tree`, and `/api/archive` stay on `-storage` during this dual-read phase. Upload and flush still write only to `-storage`.

A `/view` or `/api/file` request waits up to `-hold-timeout` (clamped at 60s) when the checkout file is missing, or when `?mtime=` is set and the checkout file is older than that Unix timestamp. `-peer-socket` is the ArborSync peer socket. While a request is held, sink watches `-sender-slave`. A query error is not treated as a dead sender; the request waits out the hold. The response is 404 when the wait ends, the slave is disconnected or stuck, or the checkout is still older than `mtime`. Too many concurrent holds return 503. `-index-rescan` is how often sink rebuilds the basename index of `-tree`; missed views rate-limit extra rebuilds.

Open http://localhost:8080/ to browse. Fetch http://localhost:8080/skill for the agent API contract (curl examples included).

## Note for the Grok Bot team

sink and the `let-that-sink-in` skill are a workaround that shows the need for a built-in version: a file handoff inside the Grok Bot app itself, secure and without a VPN or a self-hosted server. If that sounds like a good idea, please build it.
