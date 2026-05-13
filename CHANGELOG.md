# Changelog

All notable changes to artemis are documented here. Format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/); versioning
follows [Semantic Versioning](https://semver.org/spec/v2.0.0.html)
with the pre-1.0 caveat noted in `RELEASING.md`.

## [0.1.0] - 2026-05-13

### Features

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
- **handler:** Explicit octet-stream Content-Type fallback (PH1-B23) ([1b91739](https://github.com/freeCodeCamp/artemis/commit/1b917395fe7065f0f50e7cd09b37125841f43816))
- **handler:** IsCleanRelPath rejects current-dir (PH1-B22) ([a148304](https://github.com/freeCodeCamp/artemis/commit/a1483044521c6f6a535959c99dacac4fb1dea0bb))
- **sites:** Reload on k8s ConfigMap atomic rename (PH1-B19) ([250a464](https://github.com/freeCodeCamp/artemis/commit/250a464c83c1b910e69691240756830822d77d0e))
- **auth:** Detect rate-limit via header (PH1-B16) ([af3d720](https://github.com/freeCodeCamp/artemis/commit/af3d720d24951a2c2d7f259d65ab381719d880e8))
- **handler:** Tighten extractBearer to RFC 6750 (PH1-B15) ([f721a25](https://github.com/freeCodeCamp/artemis/commit/f721a25daffe4332075d02580ea1268ef726a1f3))
- **auth:** Drop shadowed JWT claim fields (PH1-B14) ([c73a233](https://github.com/freeCodeCamp/artemis/commit/c73a23320bc0956d72115c02c88b89d62844dbe7))
- **r2:** Probe rollback target via HasPrefix (PH1-B6) ([e02153e](https://github.com/freeCodeCamp/artemis/commit/e02153edeb6eb7812fe0d09c16fcb277a6db8315))
- **handler:** Reject empty Files manifest (PH1-B5) ([a76d9d3](https://github.com/freeCodeCamp/artemis/commit/a76d9d363614b804fe25d692ef27119c44857603))
- **handler:** Cap upload body via MaxBytesReader (PH1-B4) ([d29b175](https://github.com/freeCodeCamp/artemis/commit/d29b175b1bd56436e90da01c463ad60ab7e3d04a))
- **handler:** Parse deploy prefix template (PH1-B7) ([e0319f3](https://github.com/freeCodeCamp/artemis/commit/e0319f3e9f18d9555a53eabf3fb061f236bb9370))
- **config:** Validate DEPLOY_PREFIX_FORMAT shape (PH1-B8) ([e6f9809](https://github.com/freeCodeCamp/artemis/commit/e6f9809e27e876276c94a1db6b276638eb0f5ee0))

### Performance

- **r2:** Propagate Content-Length to PUT (PH1-B18) ([67c4a20](https://github.com/freeCodeCamp/artemis/commit/67c4a202a09761f16ad3eec75d96d18f7542a676))
- **handler:** Batch WhoAmI via /user/teams (PH1-B9) ([8865e84](https://github.com/freeCodeCamp/artemis/commit/8865e847974b0e325622482ddc05dd9e4b1212bb))
- **auth:** Singleflight cold-cache /user + /memberships (PH1-B2) ([1a0f9ff](https://github.com/freeCodeCamp/artemis/commit/1a0f9ff0b6a8ca0dee8263f2d91a9db8dbe30b53))
- **auth:** Negative-cache 401/403/404 (PH1-B1) ([b91d221](https://github.com/freeCodeCamp/artemis/commit/b91d221c4b0a9b3987ac14b249c81b205870f9fd))

### Refactor

- **registry:** Drop sites_yaml backend ([f115198](https://github.com/freeCodeCamp/artemis/commit/f1151989d186e730e02af732b243fe64415d1031))
- **registry:** Introduce Reader iface ([6d349d4](https://github.com/freeCodeCamp/artemis/commit/6d349d48a9daefdec79439267db2fdc82e885a2d))
- **r2:** Drop GetAlias 404 string fallback (PH1-B24) ([ee88053](https://github.com/freeCodeCamp/artemis/commit/ee88053491bf1092e82d589696fdb709e09a5d99))
- **config:** Rename mustEnv to getEnv (PH1-B21) ([4868429](https://github.com/freeCodeCamp/artemis/commit/4868429d1bc2cc4fac850c507409ee5b30853db1))
- **handler:** Typed struct context keys (PH1-B20) ([ac77dc7](https://github.com/freeCodeCamp/artemis/commit/ac77dc7313ef2105f2dda5073ce3dd4c5e36d3c7))
- **r2:** Inject clock into NewDeployID (PH1-B17) ([c589ed4](https://github.com/freeCodeCamp/artemis/commit/c589ed4dee4c66e3453f1ec602731c12018f88ef))
- Drop var _ = errors.New twins (PH1-B13) ([42d4484](https://github.com/freeCodeCamp/artemis/commit/42d44847e5ccf1fbd0ebfad11c69947ad5ca1c29))
- **handler:** Drop unused firstNonEmpty (PH1-B12) ([e848d46](https://github.com/freeCodeCamp/artemis/commit/e848d46ac8fb7570a9c1474d38c1c701e151ae6c))
- **auth:** Hash bearer tokens in cache key (PH1-B3) ([c0bf911](https://github.com/freeCodeCamp/artemis/commit/c0bf91187ca6d514ca2175ed1f3466a8834c11ba))

### Documentation

- Drop bogus --slug from sites ls examples ([8b8a769](https://github.com/freeCodeCamp/artemis/commit/8b8a76961eb0b06b31ec48611b84ed9e3aca3a09))
- Refresh sites refs for Valkey registry ([9024566](https://github.com/freeCodeCamp/artemis/commit/902456623c934deefc0512df2f3a718d0ecae727))
- Update README ([30f2842](https://github.com/freeCodeCamp/artemis/commit/30f2842acb5bf3e0e60e83b67225a435423e3b65))

<!-- generated by git-cliff -->
