# Gilir web

SolidJS dashboard for the Gilir local task queue. It talks to the same-origin
`POST /api/v1/command` endpoint and sends `actor: "user"` on every command.

## Prerequisites

- Node.js `^22.18.0 || ^24.11.0 || >=26.0.0`.
- pnpm 10.34.9, the version pinned by `packageManager` (Corepack or a global
  Vite+ `vp env` can provide it).
- Vite+ CLI 1.8.0, installed locally with the development dependencies. Run it
  with `pnpm exec vp` or through the scripts below.

## Routes

The dashboard uses browser-history routing (`@solidjs/router`), so every view has
a real URL that can be bookmarked, opened in a new tab, and reached with
Back/Forward:

- `/` - dispatch queue, including first-run repository onboarding
- `/tasks/:taskId` - task detail (canonical IDs such as `GILIR-42`)
- `/tasks/new` - new task composer
- `/projects` - combined Projects & Repos management
- `/projects/repos/new` - the add-repository form inside that screen

`App.jsx` is the router's root layout and owns the queue, service, and detail
polling plus the panel chrome; `src/pages/` holds the per-URL views. The Go
server answers direct loads and refreshes of these paths with `index.html`
(see `staticPagePath` in `internal/localhttp/localhttp.go`), while unknown
assets and `/api` paths still return 404. Keep the two path lists in sync.

## Development

```sh
pnpm install --frozen-lockfile --prod=false
pnpm dev
```

`pnpm dev` runs `vp dev`. The dev server is pinned to
`http://localhost:5173` (Vite `strictPort`). It proxies `/api` to
`http://127.0.0.1:44547` with `changeOrigin: true`.

The backend validates `Host` and `Origin` including ports. The dev proxy only
rewrites `Origin` to the backend origin when the incoming `Origin` exactly
matches the request host, the host is loopback, and the port is `5173`. Other
origins are forwarded unchanged so the backend can reject them. Run the backend
on `127.0.0.1:44547` (the default) while using the dev server; if you pass
`gilir service start --port`, point `API_TARGET` in `vite.config.js` at it.

## Production

```sh
pnpm install --frozen-lockfile --prod=false
pnpm build
```

`pnpm build` runs `vp build` and writes the static bundle to `web/dist`
(ignored by Git). The explicit `--prod=false` keeps the Vite+ development
dependencies installed when `NODE_ENV=production`. Serve that directory from
the Go process on the same origin as `/api/v1/command`; the browser then calls
the API without a proxy.

## Scripts

- `pnpm dev` - `vp dev`: Vite+ dev server with the guarded API proxy.
- `pnpm build` - `vp build`: production bundle in `dist/`.
- `pnpm preview` - `vp preview`: preview the production bundle locally.
