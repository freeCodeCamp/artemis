# Changelog

All notable changes to artemis are documented here. Format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/); versioning follows [Semantic Versioning](https://semver.org/spec/v2.0.0.html) with the pre-1.0 caveat noted in `RELEASING.md`.

## [0.3.0] - 2026-05-23

### Features

- Universe platform QOL (#2) ([14fe3a0](https://github.com/freeCodeCamp/artemis/commit/14fe3a05c6a15cd59f5cacf3330a1fcec31733f9))
- **config:** Warn on non-default GH_API_BASE at startup ([4338dd8](https://github.com/freeCodeCamp/artemis/commit/4338dd85c3305086e500c5b339fadbace80616d7))
- **handler:** WriteUpstreamError swallows upstream strings, logs server-side ([042437a](https://github.com/freeCodeCamp/artemis/commit/042437abafa724eabb0a7aeff12fd8859ed9909d))

### Bug Fixes

- **auth:** URL-escape org/teamSlug/user in GH team-membership probe ([545ec9d](https://github.com/freeCodeCamp/artemis/commit/545ec9df0d5d61bb01c085e13af2f1e34d28acda))
- **handler:** Tighten deployIDPattern to [A-Za-z0-9-]{1,64} ([a143637](https://github.com/freeCodeCamp/artemis/commit/a143637631b241c8d3510af639359cb7b70b2dac))
- **handler:** IsCleanRelPath rejects control chars + backslash ([6548629](https://github.com/freeCodeCamp/artemis/commit/6548629a3831429b92ae9ff79e1370e5e7ea0d63))

### Documentation

- **code:** Drop PH1-B phase-tracker IDs from inline comments ([35ece4a](https://github.com/freeCodeCamp/artemis/commit/35ece4ac485b96f688a6aa50fd0affc1a652cf48))
- **code:** Drop internal sprint-tracker IDs from public surface ([c82572d](https://github.com/freeCodeCamp/artemis/commit/c82572d77a57e2eaad946a85affcd7e8888f90ee))
- **code:** Drop internal RFC §-refs from code comments ([f146d0a](https://github.com/freeCodeCamp/artemis/commit/f146d0abfccecaa26f272d95bfbee057ba79b52f))
- **code:** Drop internal ADR cross-refs from code comments ([ca52b8e](https://github.com/freeCodeCamp/artemis/commit/ca52b8eb5c060e7209397a2f483fddddec337305))
- **readme:** Use octocat as example whoami login ([81bea8b](https://github.com/freeCodeCamp/artemis/commit/81bea8bec7126ecf3d2e035d62ce44a90b4cf16a))
- **release:** Drop operator-side pin steps + internal cross-refs ([2b7d6a1](https://github.com/freeCodeCamp/artemis/commit/2b7d6a1983e4cc4ca67c71c559f8baa7e47c8147))
- **deploy:** Drop internal-only post-publish runbook ([fac22fd](https://github.com/freeCodeCamp/artemis/commit/fac22fd47440ee7a85d993b60583f0aa8407b4ad))
- **deploy:** Fix healthz smoke + version check ([7594209](https://github.com/freeCodeCamp/artemis/commit/7594209512e975119b9edc532df23a7debc8eb35))
- **deploy:** Correct just release invocation ([566da2f](https://github.com/freeCodeCamp/artemis/commit/566da2fc4ce3c9b438526df00d7525dcfdc146b5))

## [0.2.0] - 2026-05-13

### Features

- **handler:** Warn promote.legacy_bare on empty-body promote ([e050648](https://github.com/freeCodeCamp/artemis/commit/e05064807f1487d38e90845293623c8061317057))
- **release:** Auto-publish GH Release on tag push ([f3bcf31](https://github.com/freeCodeCamp/artemis/commit/f3bcf319e1e223851be8631fdcc662d4e5e97f31))

### Documentation

- **deploy:** Add post-publish runbook ([c0a0be4](https://github.com/freeCodeCamp/artemis/commit/c0a0be4385a3cadaca978ee9fc4f2ba0d72bee1d))
- **release:** Clarify registry tag has no v prefix ([4798eaf](https://github.com/freeCodeCamp/artemis/commit/4798eafc85a23394979db06924106aae257a0fda))

## [0.1.0] - 2026-05-13

### Features

- **release:** Tag-trigger GHCR + embed version ([b473fcf](https://github.com/freeCodeCamp/artemis/commit/b473fcfed6c71603be346c73dffb3d62a28e7bf2))
- **handler:** Rollback expectedCurrent CAS guard ([6965b1c](https://github.com/freeCodeCamp/artemis/commit/6965b1ca915afcea959faa9a763f829a9274c7aa))
- **handler:** Promote body schema + CAS guard ([cff7939](https://github.com/freeCodeCamp/artemis/commit/cff7939109dd275333507905d6a6c6940e7d7cc0))
- **handler:** GET /api/site/{site}/alias/{mode} ([c2f40f5](https://github.com/freeCodeCamp/artemis/commit/c2f40f56b5128f6cdd6498a204366863befe68ce))
- **handler:** DELETE /api/site/{slug} ([66a084a](https://github.com/freeCodeCamp/artemis/commit/66a084aaf149a6591312cf4467f865ed499d39ee))
- **handler:** PATCH /api/site/{slug} ([cc20aee](https://github.com/freeCodeCamp/artemis/commit/cc20aeee09af8462e3700a3b5ebc7d1a8b1065a7))
- **handler:** GET /api/sites ([ba90b27](https://github.com/freeCodeCamp/artemis/commit/ba90b27806b0bb7cde6d01c19b3cc42f109e3600))
- **handler:** POST /api/site/register ([234e251](https://github.com/freeCodeCamp/artemis/commit/234e251c034e329afed6c774f5595ef508aca756))
- **registry:** Valkey reader + cache invalidation ([7b82a30](https://github.com/freeCodeCamp/artemis/commit/7b82a3021ed40ba2b8dd167de39b9d4ebafb56d4))
- **config:** REGISTRY_BACKEND env (default sites_yaml) ([93f8c95](https://github.com/freeCodeCamp/artemis/commit/93f8c95f5ab07cf7f3a0373eabc9a65509296c44))
- **registry:** Hash+set schema + atomic write ([74cb1d6](https://github.com/freeCodeCamp/artemis/commit/74cb1d6d6bf7e5bfea273a56e4f41d29d645e6b0))
- **registry:** Valkey store skeleton + connect ([b64884c](https://github.com/freeCodeCamp/artemis/commit/b64884c3ad7d6ddab126b037a66b8cfb54d3ca19))
- **sites:** Register hello-universe (bots team) ([f681431](https://github.com/freeCodeCamp/artemis/commit/f6814315589f8aaa5da4b93ebae822b628fe718c))
- **test:** Add suite setup teardown ([a3382a0](https://github.com/freeCodeCamp/artemis/commit/a3382a0461725281153060a0b79af0b17d4eff69))
- **test:** Add E2E integration suite ([f03b8b2](https://github.com/freeCodeCamp/artemis/commit/f03b8b23523ae9c85c741eb8753cf6a9043b39a6))
- **config:** Seed sites.yaml + un-gitignore ([6984190](https://github.com/freeCodeCamp/artemis/commit/698419088de10f43f138e00c3aa6e7785cd88b11))
- Initial artemis service scaffold ([c815ec4](https://github.com/freeCodeCamp/artemis/commit/c815ec42705431106c96eb38a060a1e63a3bd5ff))

### Bug Fixes

- **integration:** Broaden deployIDPattern regex ([3546a1d](https://github.com/freeCodeCamp/artemis/commit/3546a1d319204ba56e2c38d6cca3ce6809340a75))
- **direnv:** Correct r2-read envelope path ([12d9d5a](https://github.com/freeCodeCamp/artemis/commit/12d9d5ab9960f10d2b6060c33124f440faa90a6f))
- **integration:** Teardown picks newest deploy, not oldest ([9b06bff](https://github.com/freeCodeCamp/artemis/commit/9b06bff7c9ac4f2c90cfd6209feb1721d4c8b41e))
- **handler:** Explicit octet-stream Content-Type fallback ([2c731df](https://github.com/freeCodeCamp/artemis/commit/2c731dfef75b392c8695b9a57d1344e3f22b968d))
- **handler:** IsCleanRelPath rejects current-dir ([cd8b21d](https://github.com/freeCodeCamp/artemis/commit/cd8b21dae85e2532f05ae1a4d54efced090a0b5f))
- **sites:** Reload on k8s ConfigMap atomic rename ([cfaca1f](https://github.com/freeCodeCamp/artemis/commit/cfaca1f5acfa2b1b4b40086cb5981f54a215803a))
- **auth:** Detect rate-limit via header ([f63fddf](https://github.com/freeCodeCamp/artemis/commit/f63fddfca6b50f3c5be0737782e4a865a7131c3b))
- **handler:** Tighten extractBearer to RFC 6750 ([0b45ae0](https://github.com/freeCodeCamp/artemis/commit/0b45ae07db8e2cb24404ebca6fc99c666b4f544a))
- **auth:** Drop shadowed JWT claim fields ([a10a3b6](https://github.com/freeCodeCamp/artemis/commit/a10a3b679eac460c322c72281a4e8cf32c3cd36b))
- **r2:** Probe rollback target via HasPrefix ([5cfc375](https://github.com/freeCodeCamp/artemis/commit/5cfc3756cbb944c9c11cfa5ddf7bc716d1a5b409))
- **handler:** Reject empty Files manifest ([a9fdde9](https://github.com/freeCodeCamp/artemis/commit/a9fdde91595f8bf83afea22b8617a6d0063f7537))
- **handler:** Cap upload body via MaxBytesReader ([e13e8d1](https://github.com/freeCodeCamp/artemis/commit/e13e8d1de7945e1d658da97955115df06a57324d))
- **handler:** Parse deploy prefix template ([b6c49da](https://github.com/freeCodeCamp/artemis/commit/b6c49da13d6ee85cde1862c90debed5e237a275c))
- **config:** Validate DEPLOY_PREFIX_FORMAT shape ([ed926bc](https://github.com/freeCodeCamp/artemis/commit/ed926bcfec7e64a56081ddaba66b0041993e2234))

### Performance

- **r2:** Propagate Content-Length to PUT ([d42a9c4](https://github.com/freeCodeCamp/artemis/commit/d42a9c49b40c3886e0a3b48f6a59d7f716f7d398))
- **handler:** Batch WhoAmI via /user/teams ([cbb9715](https://github.com/freeCodeCamp/artemis/commit/cbb9715a5a325c39e5d70281098af46e796c4ca0))
- **auth:** Singleflight cold-cache /user + /memberships ([f1a7c63](https://github.com/freeCodeCamp/artemis/commit/f1a7c63451dc969a05aa49f8f1a82c6afcb954b7))
- **auth:** Negative-cache 401/403/404 ([e954711](https://github.com/freeCodeCamp/artemis/commit/e954711ee90720ca14d693c99aa69d966e7881d5))

### Refactor

- **registry:** Drop sites_yaml backend ([ff65d0e](https://github.com/freeCodeCamp/artemis/commit/ff65d0ee6181abeb4e9dae3f04a2fe70c31e4154))
- **registry:** Introduce Reader iface ([e820474](https://github.com/freeCodeCamp/artemis/commit/e8204743fb183ea06eb4246b17b82ea08889eb1f))
- **r2:** Drop GetAlias 404 string fallback ([02fdffa](https://github.com/freeCodeCamp/artemis/commit/02fdffa21261a1ae0ce7e8ff7d62d2d5016227de))
- **config:** Rename mustEnv to getEnv ([a807592](https://github.com/freeCodeCamp/artemis/commit/a80759216a29b4db4a0f1ab123f90a21730bae0e))
- **handler:** Typed struct context keys ([0cbfea0](https://github.com/freeCodeCamp/artemis/commit/0cbfea019643917932a7b89ace5a1557541aa091))
- **r2:** Inject clock into NewDeployID ([57fa76f](https://github.com/freeCodeCamp/artemis/commit/57fa76f10544fc48d1c5013374c7a73253631b0d))
- Drop var _ = errors.New twins ([6819f5e](https://github.com/freeCodeCamp/artemis/commit/6819f5e7b65f0ee61540b24e6f15f2b45854dff6))
- **handler:** Drop unused firstNonEmpty ([be63990](https://github.com/freeCodeCamp/artemis/commit/be63990556fe6f33e527fc9b05fb6f89027ae069))
- **auth:** Hash bearer tokens in cache key ([9f35d0b](https://github.com/freeCodeCamp/artemis/commit/9f35d0bab721e9c674e1f2b6c4b0d6a44b908b6b))

### Documentation

- Drop bogus --slug from sites ls examples ([6dd5d45](https://github.com/freeCodeCamp/artemis/commit/6dd5d45f11a3d7f2015d60a168346cb0a3bc4c94))
- Refresh sites refs for Valkey registry ([5b94bd4](https://github.com/freeCodeCamp/artemis/commit/5b94bd4a45203db452f4829d144bc02ce6345376))
- Update README ([a684515](https://github.com/freeCodeCamp/artemis/commit/a684515855b1149b073dcc6dfb3ffdbe2d68ff5e))
