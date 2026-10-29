# Roost

A lightweight, self-hosted chat server — a tiny, homebrewed alternative to Discord for
friends and family. All data stays on your own machine, in a single SQLite file.

Named "Roost" — a place where birds gather.

## Features

- Accounts with hashed passwords (bcrypt), invite-only registration after the first user
- First registered user automatically becomes the admin
- Text channels with live messages over WebSocket
- Message history with pagination, edit and delete (own messages; admins can delete any)
- Typing indicators and online presence
- Member list with online status and admin badges
- Admin panel: create/delete channels, create/revoke invite codes
- Dark, Discord-like web UI — no build tools, plain HTML/CSS/JS
- Everything in one Go binary + one SQLite database file

## Not (yet) part of it

Voice/video, direct messages, file uploads, markdown, push notifications, bots.
Think of it as a self-hosted group chat, not a full Discord replacement — yet.

## Deployment

### Your NAS (Docker / Docker Compose)

Roost ships as a single container that stores everything in `/data` inside the container
(mapped to a persistent volume or bind mount).

```bash
docker compose up -d --build
```

Then open http://YOUR-NAS-IP:3000 in a browser. On your UGREEN NAS you can use the
built-in "Docker" app: create a compose project and paste `docker-compose.yml`.

- Host port: 3000 (change it in `docker-compose.yml` if taken)
- Database lives in the `roost-data` volume; back that volume up to back up your chat

### First run

1. Open the web UI and create an account — the first account is the admin.
2. Open the gear icon (admin panel) and create an invite code.
3. Send the code or the app URL to your friends; they register with the code.
   Registration without a code is rejected.

### Running without Docker

```bash
go build -o roost .
ROOST_COOKIE_SECURE=1 ./roost -addr :3000 -data ./data
```

(-data defaults to ./data; pass an absolute path in production.)

## Remote access (important!)

The UI and API are fine over plain HTTP on your home LAN. If you want to reach the server
from outside your home, do NOT just port-forward port 3000 — logins and sessions would
traverse the internet unencrypted. Either:

- Run a VPN like Tailscale/WireGuard on your NAS and access the NAS IP from anywhere
  (recommended — zero TLS setup), or
- Put a reverse proxy (Traefik/Caddy with a Let's Encrypt certificate) in front and set
  `ROOST_COOKIE_SECURE=1` so the session cookie is marked HTTPS-only.

## Configuration

| Env var              | Default | Meaning                                |
| -------------------- | ------- | -------------------------------------- |
| `ROOST_COOKIE_SECURE`| `0`     | Set `1` when serving over HTTPS/TLS    |

CLI flags: `-addr` (default `:3000`), `-data` (default `./data`).

## Development

```bash
go test ./...
go vet ./...
go build -o roost .
```

## Security notes

- Passwords: bcrypt, cost 10. Sessions: random 256-bit tokens in HttpOnly cookies (30 days).
- Registration, login and API calls are rate-limited per IP.
- Requests with a `Origin` header that does not match the host are rejected (CSRF hardening).
- The WebSocket is served on `/api/ws` and requires a valid session cookie.

## License

MIT — see [LICENSE](LICENSE).
