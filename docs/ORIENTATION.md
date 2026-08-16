# Orientation — learn the architecture

This page is for a new contributor. It gives the read sequence for the artemis architecture. Do the steps in the sequence shown. Each step names one document and tells you what the document gives you.

## Steps

1. Read the root [`README.md`](../README.md). It tells you what artemis is and how to start the service locally.
1. Read [`ARCHITECTURE.md`](ARCHITECTURE.md), sections 1 to 3. You learn the problem artemis solves, the parts it depends on, and the deploy lifecycle.
1. Read [`ARCHITECTURE.md`](ARCHITECTURE.md), sections 4 to 9. You learn the alias model, removal and recovery, reconciliation, background work, authorization, and where each piece of state lives.
1. Read section 10 of the same document. It lists the known divergence between the code and the deployed release.
1. Read ADR-016 in the Universe platform repo (`Architecture/decisions/016-deploy-proxy.md`). It is the authoritative specification for the API surface and the per-site authorization model.
1. Read [`design/0001-durable-execution-model.md`](design/0001-durable-execution-model.md). You learn why Postgres and Hatchet are part of the design, and the safety invariants of the retention GC.
1. Use [`README.md`](README.md) in this directory as a reference. Look up routes, configuration variables, observability, and the test suites there. Do not read it end to end.

## Rules for all documents

- The source code is the primary reference. When a document in this repository and the code disagree, the code is correct.
- Read [`RELEASING.md`](RELEASING.md) only when you prepare a release. It gives the version rules and the release flow.
- Read [`design/0002-scalability-capacity.md`](design/0002-scalability-capacity.md) and [`design/0003-postgres-durability.md`](design/0003-postgres-durability.md) only when you work on capacity or durability.
