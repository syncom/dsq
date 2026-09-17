# dsq — Dead Simple Questionnaire

A single-binary questionnaire service. It serves a web form for a fixed list
of questions and stores each response as an immutable Markdown file; editing
a response writes a new version. See [product-specs.md](product-specs.md).

## Build and run

```sh
make build          # CGO_ENABLED=0 static binary ./dsq
make test

QUESTIONS_FILE=questions.example.json STORAGE_DIR=data ./dsq
```

Open `http://localhost:8080/?puid=PRODUCT-1`. In production the reverse proxy
must set the submitter header (and drop any client-supplied copy). For local
testing without a proxy, add `&submitter=alice` to the URL; the page then
sends that value in the header itself.

## Configuration

| Variable           | Default          | Description                                  |
|--------------------|------------------|----------------------------------------------|
| `PORT`             | `8080`           | HTTP listen port                             |
| `QUESTIONS_FILE`   | required         | JSON list of `{"id", "prompt"}`              |
| `STORAGE_DIR`      | required         | Flat storage directory (created if missing)  |
| `SUBMITTER_HEADER` | `X-Submitter-Id` | Trusted header carrying the submitter ID     |

## Layout

- `main.go`: configuration, wiring, graceful shutdown
- `internal/storage`: `Store` interface and the local filesystem implementation (temp file + hard link)
- `internal/questionnaire`: file format, answer normalization, versioning, per-key submission lock
- `internal/httpapi`: HTTP handlers
- `web/static`: embedded UI (`editor.js` is the contenteditable editor, `markdown.js` the Markdown conversion)
