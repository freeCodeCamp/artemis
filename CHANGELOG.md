# Changelog

All notable changes to artemis are documented here. Format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/); versioning
follows [Semantic Versioning](https://semver.org/spec/v2.0.0.html)
with the pre-1.0 caveat noted in `RELEASING.md`.

## [Unreleased]

### Documentation

- **code:** Drop PH1-B phase-tracker IDs from inline comments ([406fa39](https://github.com/freeCodeCamp/artemis/commit/406fa3944d7609096e92f428952f472554ea2a04))
- **code:** Drop internal sprint-tracker IDs from public surface ([48e9216](https://github.com/freeCodeCamp/artemis/commit/48e921639c48089d0bef65049140e7073e155cf0))
- **code:** Drop internal RFC §-refs from code comments ([8c3a8bf](https://github.com/freeCodeCamp/artemis/commit/8c3a8bf8dfbecb25fbc6e79fc79ccbd18a1528a8))
- **code:** Drop internal ADR cross-refs from code comments ([e3fd374](https://github.com/freeCodeCamp/artemis/commit/e3fd37465ec6e1028ae2262152fd910fcfad057a))
- **readme:** Use octocat as example whoami login ([53d336b](https://github.com/freeCodeCamp/artemis/commit/53d336b54184fdde6d5d44ebf5840ae54999adc9))
- **release:** Drop operator-side pin steps + internal cross-refs ([f594e99](https://github.com/freeCodeCamp/artemis/commit/f594e994df3e912a1ac0c025e5c6897654a8a7a9))
- **deploy:** Drop internal-only post-publish runbook ([1c87936](https://github.com/freeCodeCamp/artemis/commit/1c87936f4ab1e7d5b034964bfd13dbf266c5334b))
- **deploy:** Fix healthz smoke + version check ([dfdedb4](https://github.com/freeCodeCamp/artemis/commit/dfdedb48e699e04b6a594312dcebb2ebe410f129))
- **deploy:** Correct just release invocation ([f87d138](https://github.com/freeCodeCamp/artemis/commit/f87d138a2ed77cbcdec832d4cbaa816733fcff67))

## [0.2.0] - 2026-05-13

### Features

- **handler:** Warn promote.legacy_bare on empty-body promote ([d507918](https://github.com/freeCodeCamp/artemis/commit/d5079186a3e78527d25acf3dec721c1a6ea1b72f))
- **release:** Auto-publish GH Release on tag push ([602def0](https://github.com/freeCodeCamp/artemis/commit/602def06e8224dab460701c778c5ff4c4b8ffaf7))

### Documentation

- **deploy:** Add post-publish runbook ([adf07a6](https://github.com/freeCodeCamp/artemis/commit/adf07a670ad2936348580f030e46b64839183ac2))
- **release:** Clarify registry tag has no v prefix ([22fc95b](https://github.com/freeCodeCamp/artemis/commit/22fc95bbd9ca2efdba5305d85317a31b0812dfe4))

## [0.1.0] - 2026-05-13

### Features

- **release:** Tag-trigger GHCR + embed version ([8551656](https://github.com/freeCodeCamp/artemis/commit/8551656c44cc9d08fdb53f7a27c00e68f83a5082))
- **handler:** Rollback expectedCurrent CAS guard ([596b3f8](https://github.com/freeCodeCamp/artemis/commit/596b3f8d413f262bc0f52ea8c8e2272a0bfd58ea))
- **handler:** Promote body schema + CAS guard ([0976e21](https://github.com/freeCodeCamp/artemis/commit/0976e214e4c08e2b88873aa24e46847f7b4e5e6e))
- **handler:** GET /api/site/{site}/alias/{mode} ([d6914a2](https://github.com/freeCodeCamp/artemis/commit/d6914a23d04083dbcc6b7ace995cf1854bfa90a0))
- **handler:** DELETE /api/site/{slug} ([7adf917](https://github.com/freeCodeCamp/artemis/commit/7adf9172a6bcac54f6965768639411dcd6e8ec0c))
- **handler:** PATCH /api/site/{slug} ([fd6894a](https://github.com/freeCodeCamp/artemis/commit/fd6894a3ad895dda85058df0572b06a1cc67f842))
- **handler:** GET /api/sites ([f634788](https://github.com/freeCodeCamp/artemis/commit/f6347887b745f2f48fa2b570e3bb63315f1e54b7))
- **handler:** POST /api/site/register ([cc32f5b](https://github.com/freeCodeCamp/artemis/commit/cc32f5b0d2d72e0a26555cf845e937820a31ea9a))
- **registry:** Valkey reader + cache invalidation ([ba65358](https://github.com/freeCodeCamp/artemis/commit/ba65358f1241916647815875379ae7bb5ee1597d))
- **config:** REGISTRY_BACKEND env (default sites_yaml) ([f96fcf6](https://github.com/freeCodeCamp/artemis/commit/f96fcf6c1f48ffbff8ce69018ac6b645191a8fea))
- **registry:** Hash+set schema + atomic write ([cbf6c14](https://github.com/freeCodeCamp/artemis/commit/cbf6c141a0bb4cd644af1066b2f370a988b2d245))
- **registry:** Valkey store skeleton + connect ([3429c26](https://github.com/freeCodeCamp/artemis/commit/3429c26cdb0b4acb13da16a975a5451d0f46ebe4))
- **sites:** Register hello-universe (bots team) ([3c3ed0c](https://github.com/freeCodeCamp/artemis/commit/3c3ed0cdae6878ef6c80e626e3e71098eb384615))
- **test:** Add suite setup teardown ([005f2a4](https://github.com/freeCodeCamp/artemis/commit/005f2a45f7c8b593bcc7b329031da345362e165e))
- **test:** Add E2E integration suite ([434da2d](https://github.com/freeCodeCamp/artemis/commit/434da2db30f058a625a2fdc76170da5de224bb28))
- **config:** Seed sites.yaml + un-gitignore ([49d2f32](https://github.com/freeCodeCamp/artemis/commit/49d2f327af6b47d65ea18b9928195c58bae02a9d))
- Initial artemis service scaffold ([861e4c4](https://github.com/freeCodeCamp/artemis/commit/861e4c465f00f7d34e4216d3145195009d474c72))

### Bug Fixes

- **integration:** Broaden deployIDPattern regex ([f025693](https://github.com/freeCodeCamp/artemis/commit/f025693807a56c7988910956c70070b40c87c1dd))
- **direnv:** Correct r2-read envelope path ([c231767](https://github.com/freeCodeCamp/artemis/commit/c231767b1960a54ed849c0c380323fe52ca286f6))
- **integration:** Teardown picks newest deploy, not oldest ([111f349](https://github.com/freeCodeCamp/artemis/commit/111f349aa09ca15d48261ea2b7ab7599481d7db1))
- **handler:** Explicit octet-stream Content-Type fallback ([1b91739](https://github.com/freeCodeCamp/artemis/commit/1b917395fe7065f0f50e7cd09b37125841f43816))
- **handler:** IsCleanRelPath rejects current-dir ([a148304](https://github.com/freeCodeCamp/artemis/commit/a1483044521c6f6a535959c99dacac4fb1dea0bb))
- **sites:** Reload on k8s ConfigMap atomic rename ([250a464](https://github.com/freeCodeCamp/artemis/commit/250a464c83c1b910e69691240756830822d77d0e))
- **auth:** Detect rate-limit via header ([af3d720](https://github.com/freeCodeCamp/artemis/commit/af3d720d24951a2c2d7f259d65ab381719d880e8))
- **handler:** Tighten extractBearer to RFC 6750 ([f721a25](https://github.com/freeCodeCamp/artemis/commit/f721a25daffe4332075d02580ea1268ef726a1f3))
- **auth:** Drop shadowed JWT claim fields ([c73a233](https://github.com/freeCodeCamp/artemis/commit/c73a23320bc0956d72115c02c88b89d62844dbe7))
- **r2:** Probe rollback target via HasPrefix ([e02153e](https://github.com/freeCodeCamp/artemis/commit/e02153edeb6eb7812fe0d09c16fcb277a6db8315))
- **handler:** Reject empty Files manifest ([a76d9d3](https://github.com/freeCodeCamp/artemis/commit/a76d9d363614b804fe25d692ef27119c44857603))
- **handler:** Cap upload body via MaxBytesReader ([d29b175](https://github.com/freeCodeCamp/artemis/commit/d29b175b1bd56436e90da01c463ad60ab7e3d04a))
- **handler:** Parse deploy prefix template ([e0319f3](https://github.com/freeCodeCamp/artemis/commit/e0319f3e9f18d9555a53eabf3fb061f236bb9370))
- **config:** Validate DEPLOY_PREFIX_FORMAT shape ([e6f9809](https://github.com/freeCodeCamp/artemis/commit/e6f9809e27e876276c94a1db6b276638eb0f5ee0))

### Performance

- **r2:** Propagate Content-Length to PUT ([67c4a20](https://github.com/freeCodeCamp/artemis/commit/67c4a202a09761f16ad3eec75d96d18f7542a676))
- **handler:** Batch WhoAmI via /user/teams ([8865e84](https://github.com/freeCodeCamp/artemis/commit/8865e847974b0e325622482ddc05dd9e4b1212bb))
- **auth:** Singleflight cold-cache /user + /memberships ([1a0f9ff](https://github.com/freeCodeCamp/artemis/commit/1a0f9ff0b6a8ca0dee8263f2d91a9db8dbe30b53))
- **auth:** Negative-cache 401/403/404 ([b91d221](https://github.com/freeCodeCamp/artemis/commit/b91d221c4b0a9b3987ac14b249c81b205870f9fd))

### Refactor

- **registry:** Drop sites_yaml backend ([f115198](https://github.com/freeCodeCamp/artemis/commit/f1151989d186e730e02af732b243fe64415d1031))
- **registry:** Introduce Reader iface ([6d349d4](https://github.com/freeCodeCamp/artemis/commit/6d349d48a9daefdec79439267db2fdc82e885a2d))
- **r2:** Drop GetAlias 404 string fallback ([ee88053](https://github.com/freeCodeCamp/artemis/commit/ee88053491bf1092e82d589696fdb709e09a5d99))
- **config:** Rename mustEnv to getEnv ([4868429](https://github.com/freeCodeCamp/artemis/commit/4868429d1bc2cc4fac850c507409ee5b30853db1))
- **handler:** Typed struct context keys ([ac77dc7](https://github.com/freeCodeCamp/artemis/commit/ac77dc7313ef2105f2dda5073ce3dd4c5e36d3c7))
- **r2:** Inject clock into NewDeployID ([c589ed4](https://github.com/freeCodeCamp/artemis/commit/c589ed4dee4c66e3453f1ec602731c12018f88ef))
- Drop var _ = errors.New twins ([42d4484](https://github.com/freeCodeCamp/artemis/commit/42d44847e5ccf1fbd0ebfad11c69947ad5ca1c29))
- **handler:** Drop unused firstNonEmpty ([e848d46](https://github.com/freeCodeCamp/artemis/commit/e848d46ac8fb7570a9c1474d38c1c701e151ae6c))
- **auth:** Hash bearer tokens in cache key ([c0bf911](https://github.com/freeCodeCamp/artemis/commit/c0bf91187ca6d514ca2175ed1f3466a8834c11ba))

### Documentation

- Drop bogus --slug from sites ls examples ([8b8a769](https://github.com/freeCodeCamp/artemis/commit/8b8a76961eb0b06b31ec48611b84ed9e3aca3a09))
- Refresh sites refs for Valkey registry ([9024566](https://github.com/freeCodeCamp/artemis/commit/902456623c934deefc0512df2f3a718d0ecae727))
- Update README ([30f2842](https://github.com/freeCodeCamp/artemis/commit/30f2842acb5bf3e0e60e83b67225a435423e3b65))

<!-- generated by git-cliff -->
