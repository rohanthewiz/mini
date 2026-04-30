# mini

A minimal Go web service built on [rweb](https://github.com/rohanthewiz/rweb) with graceful shutdown.

## Run

```sh
go run .
```

The server listens on `:8000` by default. Override with `PORT=9000 go run .`.

## Endpoints

- `GET /` — JSON response with the current `ENV` value
- `GET /health` — liveness/readiness probe (returns `ok`)

## Docker

```sh
docker build -t mini .
docker run --rm -p 8000:8000 mini
```