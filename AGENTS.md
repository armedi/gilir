## Development

Run the service in the foreground with a development-only SQLite file:

```sh
gilir serve --db "$PWD/dev.sqlite"
```

`gilir serve` stays attached to the terminal, logs to `stderr`, and exits 0 on
`SIGINT`/`SIGTERM`. It acquires `<db>.lock` and never touches the managed
service files in the user data directory.

The managed `gilir service` lifecycle (`start`, `stop`, `restart`, `status`,
`pause`, `resume`, `concurrency`) is a singleton per user: it reads
`<dataDir>/gilir/service.json`, `service.log`, and `service.lock`. To run its
lifecycle commands in isolation, point `XDG_DATA_HOME` (or `LOCALAPPDATA` on
Windows) at a temporary directory and pass `--db` / `GILIR_DB`:

```sh
export XDG_DATA_HOME="$PWD/.dev-data"
export GILIR_DB="$PWD/dev.sqlite"
gilir service start --host 127.0.0.1 --port 44547 --db "$GILIR_DB"
gilir service status
gilir service stop
```

A second `gilir serve` or `gilir service` on the same database is refused by
`<db>.lock`; use a different `--db` (and port) to run an isolated instance.
