# `openapi.yaml` — what it is, what checks it, what does not

`docs/api/openapi.yaml` is the OpenAPI 3 description of the artemis HTTP surface. It is
hand-maintained. No generator writes it and no generator reads it at build time.

Ruling 2026-09-11: **response shapes can be machine-checked, and now are. Descriptions cannot be
checked at all.** This page names which is which, so a reader knows what a passing gate proves.

## Run the gates

```sh
just openapi
```

That runs `go test ./internal/server/ -run OpenAPI`. The same tests run in CI through `just ci`.

## What is checked

| gate | test | what it proves |
| --- | --- | --- |
| document validity | `TestOpenAPI_DocumentIsValid` | the file is a valid OpenAPI 3 document |
| route parity | `TestOpenAPI_NamesExactlyTheMountedRoutes` | the spec names exactly the routes `internal/server` mounts, no more and no fewer |
| response shape | `TestOpenAPI_ReadyzBodiesMatchTheSpec`, `TestOpenAPI_HealthzBodyMatchesTheSpec` | a real response body from the handler satisfies the schema the spec declares for that status |

The shape gate uses `openapi3filter.ValidateResponse` with `routers/legacy`. Use
`routers/legacy`, not `routers/gorillamux`; the second adds a `gorilla/mux` dependency the module
does not otherwise carry.

## What is not checked

- **Every `description` string.** Prose has no checkable form. Four `/api/site/{slug}` descriptions
  claimed an edge purge for four days after commit `4eb4c80` removed it, and no gate could have
  caught that. A description is as reliable as the last person who read it.
- **Request shapes.** `openapi3filter.ValidateRequest` exists and is not wired up.
- **Every endpoint's response.** Only `/readyz` and `/healthz` have a shape check today. The rest
  are unchecked until somebody adds one.
- **Status codes the handler can return but the spec omits.** The shape gate runs on the responses
  a test produces. A code no test produces is neither proved nor disproved.

## Add a shape check

Write a test in `internal/server` that calls the handler, then hand the recorded response to
`assertResponseMatchesSpec` (`internal/server/openapi_shape_test.go`).

```go
r := httptest.NewRequest(http.MethodGet, openAPIServer+"/your/path", nil)
w := httptest.NewRecorder()
h.YourHandler(w, r)
assertResponseMatchesSpec(t, openAPIRouter(t), r, w)
```

Build the request against `openAPIServer`. The spec declares one server
(`https://uploads.freecode.camp`) and `routers/legacy` matches on it, so a request to another host
answers `no matching operation was found`.

## Change the spec

Edit the file, run `just openapi`, and record any caller-visible change in `docs/COMPATIBILITY.md`.
The spec describes the surface as it stands; `COMPATIBILITY.md` describes what moved.
