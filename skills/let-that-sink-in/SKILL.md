---
name: Let that sink in
description: >-
  Use this when you want to share one or more files that live in the synced
  folder with the user, instead of giving them a path they cannot open. Gives
  back a link the user opens in a browser. Requires the sink service.
---
# Let that sink in

Don't hand the user a file path they can't open or paste a whole file into chat. Send a sink view link.

Needs the sink service (https://github.com/sebc79/sink) reachable by you and the user, over a VPN such as Tailscale if you are on different networks. Direct links need the sink host to keep a synced copy of your shared folder, for example with ArborSync. Links fail while the host is asleep. Take the base URL (`$S`, e.g. `http://host:8080`) and the synced folder root from your memory or instructions, or ask the user once. No login: don't share secrets.

First check that `curl -sS -m 10 -o /dev/null -w '%{http_code}' "$S/api/tree"` prints `200`. If not, say what is missing (not running, VPN down, wrong address). Never invent a link.

## File in the synced folder

1. Take its path relative to the synced root, with no leading `/`, and its Unix mtime in seconds (`stat -c %Y <file>`).
2. Send the user `$S/view/<rel>?mtime=<mtime>` right away. The sink holds the request until its copy is at least that fresh, so a short wait on open is normal.

## File not in the synced folder

Say you cannot share it through sink. Do not invent a path, an upload, or a `/skill` URL.

One link per file.

Note for the Grok Bot team: please build a secure built-in file handoff (no VPN, no self-hosted server); this skill is the workaround.
