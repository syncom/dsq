# Online Questionnaire / Survey Backend — Product Specification

## 1. Purpose

A backend service (single statically linked Go executable) that serves a
web UI for filling out a fixed, pre-configured questionnaire, stores each
filled-out response as an immutable, human-readable Markdown file, and
supports later editing (which produces a new immutable version rather than
mutating the original).

## 2. Core Functional Requirements

1. **Web interface**: presents a pre-configured, ordered list of
   questions. Each question's response is captured in a rich-text field
   that behaves like a WYSIWYG editor but serializes to Markdown, with a
   **minimum feature set**:
   - Bold and italic text
   - Ordered and unordered lists
   - Inline images pasted directly from the clipboard
   - Explicitly **not needed**: hyperlinks, titles/section headers,
     tables. The editor should not allow these to be introduced (e.g. via
     paste from a rich source like Word/Google Docs).

2. **Identity of a filled-out questionnaire**:
   - **PUID** (product unique identifier, a string) — provided as a URL
     path parameter.
   - **Submitter ID** (a string) — provided via an HTTP request header.
   - Together these identify "one respondent's questionnaire for one
     product."

3. **Storage**:
   - Each submission is saved as a Markdown file in a single **flat**
     directory (no subdirectories) at a location given by deploy-time
     configuration.
   - Filename pattern: `{PUID}-{submitterID}-{timestamp}.md`.
   - Once written, a Markdown file is **immutable** — never edited or
     overwritten in place.
   - Any images embedded in the response (pasted inline) are saved as
     separate files in the same flat directory, named with the same
     basename as their questionnaire's Markdown file.
   - Storage is local filesystem for now, but implemented behind a small
     storage interface so a future S3-backed implementation can be
     swapped in without touching business logic.

4. **Editing a submitted questionnaire**:
   - The web UI supports "resuming"/editing a previous submission.
   - This is implemented by finding the saved file with the
     **largest/latest timestamp** for a given PUID + submitter ID,
     reading it back, and pre-populating the web form from it.
   - Saving an edit does **not** modify the original file — it writes a
     brand-new file with a new (later) timestamp, per the immutability
     rule in (3). The version history is therefore the full set of files
     for that PUID + submitter ID.

5. **Implementation**:
   - Language: Go.
   - Delivered as a **single, statically linked executable** (no runtime
     dependencies) to keep deployment trivial.

## 3. Decisions Clarified During Requirements Interview

| Topic | Decision |
|---|---|
| Question list configuration | **Single, fixed** question list shared by all questionnaires/PUIDs. Its path is provided via a deploy-time environment variable (e.g. `QUESTIONS_FILE`), pointing at a JSON file. |
| Trust model for submitter identity | The `X-Submitter-Id` (configurable name) header is **trusted as-is**. Authentication/identity verification is assumed to happen upstream (e.g. a reverse proxy); the service itself does no auth. |
| Storage backend | **Local filesystem now**, but accessed only through a small `Store` interface (create-immutable / read / exists / list) so an S3-compatible backend can be added later as a second implementation of that interface. |
| Structuring multiple Q&A pairs in one file | Because the per-question editor itself cannot produce headers, the **server** injects the structure: for each question it writes a machine-readable `<!--question-id:...-->` marker followed by a human-readable `## <question prompt text>` header, then the answer's Markdown. The ID marker (not the header text) is authoritative when the file is parsed back, so a later reworded question prompt doesn't break editing of old submissions. |
| Concurrency protection | **Required.** Two near-simultaneous submissions for the same PUID + submitter ID must not both succeed / race. The service uses a non-blocking, in-process per-(PUID, submitter) lock: a submission that arrives while another is still in flight for the same key is rejected immediately (HTTP 409) rather than queued or blocked. |
| Listing/admin capability | A **version-listing endpoint** is needed: given a PUID, return all saved versions (submitter ID, timestamp, filename) for that PUID. |

## 4. Additional Design Decisions (flagged, not yet confirmed)

These are reasonable implementation choices made to fill gaps in the spec;
call them out for review rather than treating them as locked-in:

- **Filename sanitization vs. exact identity**: PUID and submitter ID are
  arbitrary strings but must appear in a filesystem-safe filename. The
  service sanitizes unsafe characters for the *filename* only, and
  separately stores the **exact, original** PUID / submitter ID / submit
  timestamp inside the file itself, in a machine-readable comment block
  at the top (base64-encoded JSON, so no embedded content can corrupt the
  comment or collide with the sanitization). All lookups (find latest,
  list versions) are done by reading and matching this embedded metadata,
  not by re-parsing the filename — this avoids any correctness bugs from
  sanitization collisions between different PUIDs/submitter IDs.
- **Timestamp format**: UTC, fixed-width, lexicographically sortable
  (e.g. `20060102T150405.000000000Z`), so "latest by timestamp" can be
  found by string comparison as well as by parsing.
- **Editor implementation**: to keep the binary dependency-free (no CDN
  script, no JS build pipeline required), the WYSIWYG editor is a small
  hand-rolled `contenteditable`-based component (not a third-party
  library like TipTap/Quill), restricted to bold, italic, ordered/
  unordered lists, and image paste. Pasted rich content (e.g. from Word)
  is sanitized on paste to strip anything outside that feature set
  (links, headers, tables, etc.).
  - *Alternative not chosen*: wire in a full-featured JS editor library
    via CDN. Rejected by default because it adds a runtime internet
    dependency, which cuts against the "simple single-binary deployment"
    goal — but this is a reasonable thing to revisit if a richer editor
    is wanted later.
- **Image upload flow**: pasted images are held client-side as in-memory
  blobs with a random token; the editor's Markdown for that spot is
  `![](pending:TOKEN)`. On submit, the whole form (answers JSON + image
  binaries) is sent as `multipart/form-data`; the server assigns each new
  image its final filename (`{md-basename}-N.<ext>`), saves it
  immutably, and rewrites the `pending:TOKEN` references to the final
  filename before writing the questionnaire's Markdown file.
- **Image reuse across versions**: images are never duplicated or
  renamed on a re-edit. If an edited version still contains an image from
  an earlier version, the new Markdown file just references the
  already-stored image file by its original filename. Only genuinely new
  pasted images (in that edit) get a new filename based on the new
  version's basename. (Reading requirement 3 literally — "prefixed with
  the same basename of the markdown file" — as describing the naming
  applied *at the time an image is uploaded*, not a requirement to rename
  or copy old images into every later version.)
- **Image access control**: images are served by an unauthenticated
  `GET /api/files/{filename}` endpoint (a capability-URL style access —
  you need to already know the exact generated filename). This matches
  the "no auth in this service" trust model from the interview. If
  stronger access control is needed later, this endpoint is the place to
  add it.
- **Lock lifetime**: the in-process lock table keyed by (PUID, submitter)
  grows for the life of the process (never evicted). Acceptable for
  typical usage; would want a cleanup/eviction strategy if the number of
  distinct (PUID, submitter) pairs handled over a process's lifetime gets
  very large.
- **Finding "latest" / listing versions**: implemented by scanning all
  files in the flat directory and reading each file's embedded metadata
  comment. This is simple and correct but O(number of files) per request;
  fine for a moderate number of submissions, but would benefit from an
  index (e.g. a small embedded DB, or per-PUID subdirectories) if the
  store grows very large.

## 5. HTTP API (as implemented)

- `GET /api/questions`
  Returns the configured question list: `[{"id": "...", "prompt": "..."}]`.

- `POST /api/questionnaires/{puid}`
  Header: `X-Submitter-Id: <submitter>`
  Body: `multipart/form-data` with:
  - `answers` — JSON string `{ "<questionId>": "<markdown>", ... }`
  - zero or more file parts named `image:<token>`, one per pasted image
    referenced as `pending:<token>` in some answer's Markdown.
  Responses: `201 Created` with `{puid, submitterId, timestamp, filename}`;
  `400` for bad input; `409` if a submission for this PUID+submitter is
  already in flight.

- `GET /api/questionnaires/{puid}/latest`
  Header: `X-Submitter-Id: <submitter>`
  Returns the most recent version's answers, pre-formatted for the editor:
  `{ "timestamp": "...", "answers": { "<questionId>": "<markdown>", ... } }`
  (empty `answers` object if no prior submission exists).

- `GET /api/questionnaires/{puid}/versions`
  Returns all saved versions for a PUID across all submitters, newest
  first: `[{ "submitterId": "...", "timestamp": "...", "filename": "..." }, ...]`.

- `GET /api/files/{filename}`
  Serves a previously saved image file by its exact filename.

- `GET /*`
  Serves the embedded static web UI (HTML/CSS/JS).

## 6. Markdown File Format

```markdown
<!--questionnaire-meta:BASE64({"puid":"...","submitter":"...","timestamp":"..."})-->

<!--question-id:BASE64(questionId1)-->
## <question 1 prompt text>

<question 1 answer, as Markdown — bold/italic/lists/images only>

<!--question-id:BASE64(questionId2)-->
## <question 2 prompt text>

<question 2 answer...>
```

## 7. Deploy-Time Configuration (environment variables)

- `PORT` — HTTP listen port (default `8080`).
- `QUESTIONS_FILE` — path to a JSON file: `[{"id": "...", "prompt": "..."}]`. **Required.**
- `STORAGE_DIR` — path to the flat storage directory (created if missing). **Required.**
- `SUBMITTER_HEADER` — name of the header carrying the submitter identity (default `X-Submitter-Id`).

## 8. Non-Functional Notes

- Implemented using only the Go standard library (no third-party
  dependencies), so `go build` requires no network access and produces a
  single static executable — satisfies the deployment-simplicity goal.
- Uses Go 1.22+ `net/http.ServeMux` pattern routing
  (`"GET /api/questions"`, path wildcards like `{puid}`) and
  `http.FileServerFS` for the embedded static assets.
- Immutability of saved files is enforced at the storage layer: a file is
  written to a temp path and then hard-linked into its final name, which
  fails (rather than silently overwriting) if the destination already
  exists.
