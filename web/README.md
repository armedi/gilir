# Gilir web

SolidJS dashboard for the Gilir local task queue. It talks to the same-origin
`POST /api/v1/command` endpoint and sends `actor: "user"` on every command.

## Development

```sh
npm ci --include=dev
npm run dev
```

The dev server is pinned to `http://localhost:5173` (Vite `strictPort`). It
proxies `/api` to `http://127.0.0.1:4718` with `changeOrigin: true`.

The backend validates `Host` and `Origin` including ports. The dev proxy only
rewrites `Origin` to the backend origin when the incoming `Origin` exactly
matches the request host, the host is loopback, and the port is `5173`. Other
origins are forwarded unchanged so the backend can reject them. Run the backend
on `127.0.0.1:4718` while using the dev server.

## Production

```sh
npm ci --include=dev
npm run build
```

`npm run build` writes the static bundle to `web/dist` (ignored by Git). Serve
that directory from the Go process on the same origin as `/api/v1/command`; the
browser then calls the API without a proxy.

## Scripts

- `npm run dev` - Vite dev server with the guarded API proxy.
- `npm run build` - production bundle in `dist/`.
- `npm run preview` - preview the production bundle locally.
