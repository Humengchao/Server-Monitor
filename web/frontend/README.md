# Server Monitor frontend

React, TypeScript and Vite provide the dashboard, host operations, terminals,
file manager and AI workspace. Run these commands in `web/frontend`.

## Development

Use Node.js 22.13 or later:

```sh
npm ci
npm run dev
```

Vite listens on port 5173. `/api` requests, including WebSockets, proxy to
`http://localhost:8080`. Set `VITE_API_PROXY` to override that backend address.
In PowerShell, set `$env:VITE_API_PROXY = 'http://localhost:19527'` before starting
Vite. The backend needs its own database and environment configuration; see
[deployment documentation](../../docs/web-deployment.md).

Set `VITE_API_URL` at build time to use a different API origin or path prefix,
such as `https://api.example.com/monitor/api` or `/monitor/api`. HTTP, public
status, SSH and Docker terminals share this prefix; HTTPS addresses use WSS.
For separate origins, configure backend CORS to allow the frontend origin.
Terminal WebSockets authenticate with the session's Bearer token.
Production management requires HTTPS. Do not put
credentials or tokens in these environment variables.

## Checks and build

```sh
npm run lint
npm run build
npm run preview -- --host 127.0.0.1
```

`dist/` contains production assets. Preview is for local verification;
production uses the repository's Nginx configuration. Terminal, chart and
editor dependencies load on demand.

## Browser tests

```sh
npx playwright install chromium
npm run build
npx playwright test --workers=2
```

Playwright starts a production preview on port 4173 and mocks business APIs.
For targeted checks, pass a file such as `tests/server-form.spec.ts`.
`SMOKE_BASE_URL` reuses an existing server instead; its readiness check can
contact that server, so only configure an intended target.
Keep generated traces, screenshots, local build variants and private
environment files out of version control.
