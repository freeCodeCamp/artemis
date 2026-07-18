# Changelog

All notable changes to artemis are documented here. Format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/); versioning follows [Semantic Versioning](https://semver.org/spec/v2.0.0.html) with the pre-1.0 caveat noted in `docs/RELEASING.md`.

## [1.6.2](https://github.com/freeCodeCamp/artemis/compare/v1.6.1...v1.6.2) (2026-07-18)


### Bug Fixes

* **migrate:** log shutdown-abort instead of escalating transient ([0d9bdfb](https://github.com/freeCodeCamp/artemis/commit/0d9bdfb0803daeb806dab8e1ce0472c4256e69a7))

## [1.6.1](https://github.com/freeCodeCamp/artemis/compare/v1.6.0...v1.6.1) (2026-07-17)


### Bug Fixes

* **handler:** redact actor fields on approve/reject for non-staff ([04284c6](https://github.com/freeCodeCamp/artemis/commit/04284c63871c88683a669efa6b85ceaebc45360e))

## [1.6.0](https://github.com/freeCodeCamp/artemis/compare/v1.5.0...v1.6.0) (2026-07-17)


### Features

* **observability:** escalate sustained transients ([0fee610](https://github.com/freeCodeCamp/artemis/commit/0fee610c00ecff4855e66b925abcc636ca171b84))
* **pg:** concurrent no-tx migration path + audit 0008 ([0d7442b](https://github.com/freeCodeCamp/artemis/commit/0d7442b63a79a7f802d2adc9d9eccbae8b076aae))


### Bug Fixes

* **gc:** move tombstone audit outside site lock ([b4648fd](https://github.com/freeCodeCamp/artemis/commit/b4648fd878d8411e6c9434a034347ffd2713ea75))
* **handler:** gate actor-identity fields to staff ([85f4455](https://github.com/freeCodeCamp/artemis/commit/85f4455f9c9b0c624042b0989cf86ce36ad8c33b))
* **handler:** redact CreatedBy on SiteUpdate for non-staff ([0e5a04f](https://github.com/freeCodeCamp/artemis/commit/0e5a04f63b687ea345703b9586c528b905f1c351))
* **main:** wire RepoGH independent of repo-create feature ([0c3fc8b](https://github.com/freeCodeCamp/artemis/commit/0c3fc8bd64f909aad6e7fa007d497b9bf56a830e))
* **observability:** treat deadline/55P03 as transient ([fbde5c1](https://github.com/freeCodeCamp/artemis/commit/fbde5c189fce16b966ecbb432616e90d927f8271))
* **pg:** don't hold outbox tx across publish ([155e7df](https://github.com/freeCodeCamp/artemis/commit/155e7df458f6f7b4488dc7fb96442f2b80fd02ac))
* **reconcile:** emit one aggregated capture per tick ([4030f53](https://github.com/freeCodeCamp/artemis/commit/4030f5394ebc812cc3e5bbd68fd67daf7f13da69))
* **scheduler:** break reconcile loop on exhausted budget ([4d2d50d](https://github.com/freeCodeCamp/artemis/commit/4d2d50da67d02735c181e0395d0d569f9e473187))
* **valkey:** bound-retry boot connect for store + teamcache ([66fdf00](https://github.com/freeCodeCamp/artemis/commit/66fdf00eb84ec0309214b51c029d7fe938db6a40))

## [1.5.0](https://github.com/freeCodeCamp/artemis/compare/v1.4.0...v1.5.0) (2026-07-15)


### Features

* **gc:** audit each tombstone in sweep and reconcile ([3af2547](https://github.com/freeCodeCamp/artemis/commit/3af2547e7a6ba767fcb15a2102f8e53b085a12f5))
* **handler:** add GET /api/audit trail endpoint ([4690bf5](https://github.com/freeCodeCamp/artemis/commit/4690bf50b71045fade352f2e68830c455287c059))
* **handler:** audit repo lifecycle actions to audit_log ([4cdf82e](https://github.com/freeCodeCamp/artemis/commit/4cdf82ef92a8aba4369f3418dbd42ed26ca5602d))
* **handler:** join finalize actor onto deploy list ([9416712](https://github.com/freeCodeCamp/artemis/commit/9416712e876fd42236e8edf5cd4897851d97b2c2))
* **pg:** add audit_log request_id column and actor index ([6807e86](https://github.com/freeCodeCamp/artemis/commit/6807e86f44b8eb3b15fb7443f48eced30d06745a))
* **pg:** add ListAudit query with filters and pagination ([6e9184c](https://github.com/freeCodeCamp/artemis/commit/6e9184c9af4f2e85a416993af2afadec238932bb))


### Bug Fixes

* **gc:** raise sentry issue on background audit-write failure ([1b607b2](https://github.com/freeCodeCamp/artemis/commit/1b607b22b15ba2f8b03122e2ae7c44550e893640))
* **handler:** cap repo reject reason length before audit ([a4bd4d5](https://github.com/freeCodeCamp/artemis/commit/a4bd4d5e66d15ec946d91f08f15b06ae20fef5a9))
* **handler:** capture repo name in reject and delete audit ([0fed9c5](https://github.com/freeCodeCamp/artemis/commit/0fed9c58db45ad9d024194f5115e37bfbe3d37a4))
* **handler:** gate /api/audit on Universe staff team ([d79e90a](https://github.com/freeCodeCamp/artemis/commit/d79e90a5617df3a500a6b2c94ee57d6baed52aed))
* **observability:** enable tracing so transactions are recorded ([822122d](https://github.com/freeCodeCamp/artemis/commit/822122d1535f8603de6a04e611ccc02ac1e38b7b))

## [1.4.0](https://github.com/freeCodeCamp/artemis/compare/v1.3.0...v1.4.0) (2026-07-14)


### Features

* **gc:** audit each purged deploy as system:gc ([3291c36](https://github.com/freeCodeCamp/artemis/commit/3291c36b80bcd2d95b8dc07eab4d4b5210f9e57e))
* **gc:** page on reconcile aliased_missing drift via Sentry issue ([32793d4](https://github.com/freeCodeCamp/artemis/commit/32793d4ba31f5a5e3d04f3da3db83e855b094723))
* **handler:** add h.audit helper + AuditStore interface ([92f01de](https://github.com/freeCodeCamp/artemis/commit/92f01dec9a5501aee72ed2c9eab994dc5e7958f8))
* **handler:** add HTTP request/duration/in-flight metrics ([c5e5c08](https://github.com/freeCodeCamp/artemis/commit/c5e5c08da328fc1139e325047937fa73a242174e))
* **handler:** add logAction helper; log deploy.init with actor ([0147f81](https://github.com/freeCodeCamp/artemis/commit/0147f81416060298aacd3d978e5e6757d2256da7))
* **handler:** add Sentry breadcrumbs at promote/finalize checkpoints ([fb43d98](https://github.com/freeCodeCamp/artemis/commit/fb43d9809d54832d0b8d834a1193f80cb929293c))
* **handler:** audit all 11 destructive actions incl SiteRegister ([f454a7b](https://github.com/freeCodeCamp/artemis/commit/f454a7bb2b2626ae726decab773085b0f29a4576))
* **handler:** capture chi route pattern into access-log + Scope ([d95c3c8](https://github.com/freeCodeCamp/artemis/commit/d95c3c89f6c84de60ca48facc6aeafcb1a1d2048))
* **handler:** convert mutating handlers to logAction with actor ([dc75f2f](https://github.com/freeCodeCamp/artemis/commit/dc75f2fbc1227647911dabdfe45302844fb802aa))
* **handler:** emit action_total metric from logAction ([2f262d4](https://github.com/freeCodeCamp/artemis/commit/2f262d48605f9eaa4163e81155d3d34eaa7cba67))
* **handler:** enrich upstream-error log + Sentry with actor/site/deployId ([6f3cd6a](https://github.com/freeCodeCamp/artemis/commit/6f3cd6a7e59023adad49b3a82588afc797930642))
* **handler:** log deploy.upload/finalize success with actor + bytes ([38cf481](https://github.com/freeCodeCamp/artemis/commit/38cf4812f9b559d6e6a7ed12aaf2a1552b4c9a28))
* **handler:** log site.update with actor + before/after teams ([b10095f](https://github.com/freeCodeCamp/artemis/commit/b10095f95d3fefd974280b3847b9a8196fadbb54))
* **handler:** set Sentry user on deploy-JWT routes ([71bdbb6](https://github.com/freeCodeCamp/artemis/commit/71bdbb6fd28923d84825c46416195eed7cbb7adc))
* **handler:** trace spans + breadcrumbs on destructive flows ([e923416](https://github.com/freeCodeCamp/artemis/commit/e9234165088f7c2932675838a35912dc3873df20))
* **handler:** trace_id exemplar on http duration histogram ([3291608](https://github.com/freeCodeCamp/artemis/commit/32916080fecc38e6dcb97a587024fe0c18a1c5a6))
* **hatchet:** capture worker task panics to Sentry via WithPanicHandler ([64cbc0d](https://github.com/freeCodeCamp/artemis/commit/64cbc0d14de937846eab2a8c7ee6f65f1caafbcf))
* **metrics:** add audit_events_total counter surface ([3c261dd](https://github.com/freeCodeCamp/artemis/commit/3c261ddb639781a41d06f1ee41ae9b23579b870a))
* **metrics:** extend upload buckets past 10s + build_info gauge ([a0ee63f](https://github.com/freeCodeCamp/artemis/commit/a0ee63fcf2a8e7466500ad1be070e56a5d8b87c4))
* **metrics:** label tombstoned deploys by trigger (manual|scheduled) ([7f8f26e](https://github.com/freeCodeCamp/artemis/commit/7f8f26e52287eb1f5ca4a74d0772501a540eb5e4))
* **observability:** force-sample destructive actions by raw method+path ([0aca05d](https://github.com/freeCodeCamp/artemis/commit/0aca05dff13c3ae47c6b8901821536f181f8bdba))
* **observability:** harden Sentry ClientOptions (ServerName, breadcrumbs) ([fb8f193](https://github.com/freeCodeCamp/artemis/commit/fb8f1934349aa6c17f700c4de2e51022b0b59fc5))
* **observability:** log workflow panics to slog, not just Sentry ([dab5d89](https://github.com/freeCodeCamp/artemis/commit/dab5d897ccb63f601777c329266db733b22bbac6))
* **observability:** scrub breadcrumb message + nested data instead of dropping ([bb1f12d](https://github.com/freeCodeCamp/artemis/commit/bb1f12ddf4699c7394dd84288f0e967f6a115306))
* **observability:** scrub stdout logs via shared ScrubText/ScrubAttrs ([5e44aa8](https://github.com/freeCodeCamp/artemis/commit/5e44aa83430ea5341a5dd479e460b32e3c967f8d))
* **pg:** add append-only audit_log table + RecordAudit ([025cc73](https://github.com/freeCodeCamp/artemis/commit/025cc738966607b252464d482600da210bf7cfb5))
* **server:** retag Sentry tx name to method + chi route pattern ([e5d81c8](https://github.com/freeCodeCamp/artemis/commit/e5d81c89894f05aa248c8e821b3b18f74211eb41))
* **telemetry:** add reqID RoundTripper; thread reqID into pg lock/retry logs ([b66d532](https://github.com/freeCodeCamp/artemis/commit/b66d5326fee99501fca03d2a51fd8e1c7215844b))
* **telemetry:** add request Scope with mutex-guarded setters ([cd8028c](https://github.com/freeCodeCamp/artemis/commit/cd8028cb09a5a7622f8a61dc1974d9f5892da8f6))
* **telemetry:** add trace_id/span_id to log lines from active span ([560b198](https://github.com/freeCodeCamp/artemis/commit/560b198217df7b2475a3d71eeebd76451d41773a))
* **telemetry:** add WithSpan; instrument DeployFinalize dep calls ([625af4c](https://github.com/freeCodeCamp/artemis/commit/625af4cce194907fb4b6cb8096620f854852c237))
* **telemetry:** inject request-scoped attrs into every log line ([35d5234](https://github.com/freeCodeCamp/artemis/commit/35d523450582d3ff556c0900ae013b80f2a81aaf))
* **telemetry:** propagate sentry-trace on outbound GitHub calls ([b5f921a](https://github.com/freeCodeCamp/artemis/commit/b5f921a4e35756d78fb357af6ccb240efcd4d53e))
* **worker:** add duration histograms for workflows + relay ([985353d](https://github.com/freeCodeCamp/artemis/commit/985353d1610ad5397041004b516811370f28281f))
* **worker:** mint run_id scope for cron/relay workflow firings ([2257f01](https://github.com/freeCodeCamp/artemis/commit/2257f01aacf7f2f3b219c5288de41fc1fcba0208))
* **worker:** Sentry Crons check-ins on reconcile + purge crons ([7e24151](https://github.com/freeCodeCamp/artemis/commit/7e24151273451048e9a0ff4850458b84997667a1))


### Bug Fixes

* **backfill:** keep first genuine byte error, ignore trailing cancel ([9828d4a](https://github.com/freeCodeCamp/artemis/commit/9828d4a47b0b05c664a30066d81792d11f0600be))
* **gc:** bound reconcile-scheduler publish with a deadline ([091887f](https://github.com/freeCodeCamp/artemis/commit/091887fa4cece9f38032e4fa8e5e1ea241144bd6))
* **gc:** derive live-alias re-read key via &lt;site&gt; substitution ([fc1df86](https://github.com/freeCodeCamp/artemis/commit/fc1df867c507a4b8a86c283ee1c8ca3bd8a79d00))
* **gc:** downgrade self-healing reconcile/lock logs to Warn ([4441eb1](https://github.com/freeCodeCamp/artemis/commit/4441eb1a44b8db1ec2c83722e5472f34f46b6f54))
* **gc:** guard purge double-count + thread ctx through gc/handler logs ([64057ff](https://github.com/freeCodeCamp/artemis/commit/64057ff7af631b9921264bdb76a0495057fa8e9d))
* **gc:** stop paging on self-healed aliased drift ([1df5dbb](https://github.com/freeCodeCamp/artemis/commit/1df5dbb1a7cc62bf93d88ac53fac17efd8041fd2))
* **handler:** drop duplicate actor log key ([2127362](https://github.com/freeCodeCamp/artemis/commit/2127362acdd4aa636cdd57eca811d9d75b254b24))
* **handler:** lock SiteUpdate to fix audit race ([f87c3c8](https://github.com/freeCodeCamp/artemis/commit/f87c3c8f9f7ded386c0d4002cb08d1320fde3633))
* **handler:** map GitHub rate-limit to 429 at re-probe sites ([40ed828](https://github.com/freeCodeCamp/artemis/commit/40ed8280dcd8d85605426d947dcb10bb5469d0c7))
* **handler:** populate access-log actor via shared request Scope ([9c74c75](https://github.com/freeCodeCamp/artemis/commit/9c74c75d9550b9ec18e31f2aec80b387f7615355))
* **handler:** record real deploy bytes in delete tombstone ([df37b11](https://github.com/freeCodeCamp/artemis/commit/df37b1150715fef6c23d18967202680de96189dc))
* **internal:** do not init on non-template ([#20](https://github.com/freeCodeCamp/artemis/issues/20)) ([51def46](https://github.com/freeCodeCamp/artemis/commit/51def462338a370ba376872ff4577a987f21f5ac))
* **main:** wire h.Audit so audit_log persists for HTTP actions ([f364f1a](https://github.com/freeCodeCamp/artemis/commit/f364f1ac851ea3e0903c10f7bf5449bcc510c53d))
* **observability:** scrub secrets in error/stringer log attrs ([5808b95](https://github.com/freeCodeCamp/artemis/commit/5808b9591572675c9e700d9418f3201a6733bccb))
* **pg:** release and surface stranded advisory unlock ([00631fd](https://github.com/freeCodeCamp/artemis/commit/00631fd8930cc7f983453c2e8672025dcf6929dd))
* **readyz:** swap log levels to match client impact ([31633b5](https://github.com/freeCodeCamp/artemis/commit/31633b5ef7d1efc475ce3fb33e0df345d11b46cd))
* **worker:** add reconcile-scheduler cron producing site.reconcile events ([fc72a64](https://github.com/freeCodeCamp/artemis/commit/fc72a64e09f4265202df8eb72421e53fcee905f2))
* **worker:** bound relay pass so a Hatchet stall frees pool + locks ([9473578](https://github.com/freeCodeCamp/artemis/commit/94735788ccfa655c0557b18d7db18a3950dac4e5))
* **worker:** claim outbox rows FOR UPDATE SKIP LOCKED to stop cross-replica dup ([cf9644a](https://github.com/freeCodeCamp/artemis/commit/cf9644ac7fd363fc471fd24bbdb891234a15e8e7))
* **worker:** scale relay timeout with batch size ([865a4fb](https://github.com/freeCodeCamp/artemis/commit/865a4fb5000f7650ce73f7bf03f42981dd253a54))

## [1.3.0](https://github.com/freeCodeCamp/artemis/compare/v1.2.2...v1.3.0) (2026-07-05)


### Features

* **handler:** add deploy restore and trash-list endpoints ([33a6ede](https://github.com/freeCodeCamp/artemis/commit/33a6edea8d3ca756a7b85ccf7cd4de699676618a))


### Bug Fixes

* **gc:** detach tombstone-move from workflow context cancellation ([d07f155](https://github.com/freeCodeCamp/artemis/commit/d07f15559e8274e947e07c95ec84e6deae7d0f66))
* **gc:** reuse one advisory-lock conn across a GC run's per-move locks ([fbc3bc7](https://github.com/freeCodeCamp/artemis/commit/fbc3bc778886a16dce5fe197a64e6c179cfa57bc))
* **handler:** close purge-vs-finalize race with in-lock site re-check ([beeb786](https://github.com/freeCodeCamp/artemis/commit/beeb7866c6d0bf36dd099526407e784e7e3890c3))
* **handler:** reject deploy JWT when scoped site is unregistered ([ec9188c](https://github.com/freeCodeCamp/artemis/commit/ec9188c4b2df3b243a8e62908e3d6b38e6f5bd93))

## [1.2.2](https://github.com/freeCodeCamp/artemis/compare/v1.2.1...v1.2.2) (2026-07-04)


### Bug Fixes

* **backfill:** one aggregate byte-failure capture per run ([9fcf4f6](https://github.com/freeCodeCamp/artemis/commit/9fcf4f6c4de18c1abdcff30f1933846f55575725))
* **backfill:** report bytes soft-fail to Sentry (grouped) ([6b69984](https://github.com/freeCodeCamp/artemis/commit/6b699848c212ebfc099483d9eb2baeaec2de36b5))
* **backfill:** unwrap last error so shutdown cancel suppressed ([de9dac4](https://github.com/freeCodeCamp/artemis/commit/de9dac40179600aa3a2275f49444691f255e53e5))
* **handler:** edge-triggered readyz paging via reset-latch ([51fead1](https://github.com/freeCodeCamp/artemis/commit/51fead1885df1760a59d5742e31d205064f1254d))
* **handler:** readyz streak-based paging for sustained outages ([9167af7](https://github.com/freeCodeCamp/artemis/commit/9167af7e330560d08bd80013ad1f2add0420ccb8))
* **pg:** zero bytes upsert must not clobber a known value ([190f34f](https://github.com/freeCodeCamp/artemis/commit/190f34f55f01c0c582e33a24f1cc03306caa1642))

## [1.2.1](https://github.com/freeCodeCamp/artemis/compare/v1.2.0...v1.2.1) (2026-07-04)


### Bug Fixes

* **backfill:** soft-fail bytes probe, never crash fleet backfill ([36eb530](https://github.com/freeCodeCamp/artemis/commit/36eb53004267671dbc35983d0a61fc1d4ae0d139))
* **handler:** page Sentry on sustained readyz outage ([73c6d9b](https://github.com/freeCodeCamp/artemis/commit/73c6d9b3c00aaadd3e41f74089b77fea8e4a8ef7))
* **handler:** restore R2 signal on finalize bytes soft-fail ([98ce16c](https://github.com/freeCodeCamp/artemis/commit/98ce16c752ade104ef207b2e73679e556590e11f))

## [1.2.0](https://github.com/freeCodeCamp/artemis/compare/v1.1.1...v1.2.0) (2026-07-04)


### Features

* **gc,server:** gc-site advisory lock per-move + chi request timeout ([c94a8a8](https://github.com/freeCodeCamp/artemis/commit/c94a8a8ef79988d4369b89170520f3b8a72811bf))
* **gc:** blast-cap partial-progress + capped metric; grace 1h-&gt;72h ([86d0e75](https://github.com/freeCodeCamp/artemis/commit/86d0e7584fb866ca80727378eeb64cb7ee6a49a0))
* **pg,r2,backfill:** populate deploys.bytes + supersede-stamp test ([25c7f8b](https://github.com/freeCodeCamp/artemis/commit/25c7f8bb956b0805c41e16d69b48cba3129e07a3))


### Bug Fixes

* **handler:** 409 on site-lock timeout; quiet transient sentry ([19669ee](https://github.com/freeCodeCamp/artemis/commit/19669eeab8ed984b9064adb63fbb050a67b4af43))
* **handler:** deploy.bytes is best-effort, never gates finalize (DHP-1) ([5ac32e6](https://github.com/freeCodeCamp/artemis/commit/5ac32e6377c8d0b5a1cbf9692e73ba65c27730da))
* **handler:** detach destructive purge/delete moves from request deadline (TMO-1/2) ([4290e2a](https://github.com/freeCodeCamp/artemis/commit/4290e2a39f9edcdf5c5acfd0ea2c12094462330b))

## [1.1.1](https://github.com/freeCodeCamp/artemis/compare/v1.1.0...v1.1.1) (2026-06-06)


### Bug Fixes

* **gc,handler:** R2-authoritative alias re-read + per-site advisory lock on destructive ops ([f12aad5](https://github.com/freeCodeCamp/artemis/commit/f12aad57f44fd9b3a95d0116e9e08891d30d1a4a))
* **gc:** per-deploy alias-release hold replaces site-blanket freshAliasMove ([74c76ca](https://github.com/freeCodeCamp/artemis/commit/74c76ca0e7c5f2057f2a0c4068351a21d8de8429))
* **pg,handler:** dedicated lock connection + in-lock CAS preflight reads ([8b3b1a0](https://github.com/freeCodeCamp/artemis/commit/8b3b1a0c7182c785c63df63e2b8b42e8c7a04012))

## [1.1.0](https://github.com/freeCodeCamp/artemis/compare/v1.0.0...v1.1.0) (2026-06-06)


### Features

* **pg:** bounded boot connect retry with backoff ([068e455](https://github.com/freeCodeCamp/artemis/commit/068e45541138dff76e330ce0859bd2ad12c89024))


### Bug Fixes

* **boot:** shutdown-aware exit, bounded lock waits ([f107e32](https://github.com/freeCodeCamp/artemis/commit/f107e3277dd5e356f479be4f222d36681e1edd8d))
* **handler:** canonicalize site keys to R2 dirname at GC boundary ([15757f5](https://github.com/freeCodeCamp/artemis/commit/15757f5b2e861d65f1baf08240e2c565d9a279f0))
* **handler:** write deploy index + alias rows through to PG on finalize/promote/rollback ([2e8ed88](https://github.com/freeCodeCamp/artemis/commit/2e8ed8801fd7fbcf04ca4e78608d487d757151d5))

## [1.0.0](https://github.com/freeCodeCamp/artemis/compare/v0.8.0...v1.0.0) (2026-06-05)


### Features

* **auth:** consult durable teamcache before GitHub team probe ([6355ea7](https://github.com/freeCodeCamp/artemis/commit/6355ea7902ca7ad8c192d765814f7b8b12d7f17e))
* **backfill:** one-shot BACKFILL_ON_BOOT R2-to-PG index runner ([a185b51](https://github.com/freeCodeCamp/artemis/commit/a185b51a4337679f2e5678215ca935eeee64304d))
* **boot:** inject pg.Repo as handler Outbox + Tombstones (TrashPrefixBase) ([0dcaaa5](https://github.com/freeCodeCamp/artemis/commit/0dcaaa5ba0d4aaa686786c4c2957a57bcf1646ca))
* **boot:** open pg pool + run migrations gated on DATABASE_URL ([55a535e](https://github.com/freeCodeCamp/artemis/commit/55a535ec1ed1b153d50dcdba6579cd8d903f3d6b))
* **boot:** register + start gc workflows on the Hatchet worker ([c8fd6bf](https://github.com/freeCodeCamp/artemis/commit/c8fd6bf2e3dd59a8ee00e724ec542cb870c4c3b0))
* **boot:** run outbox-relay ticker loop draining to the Hatchet adapter ([7d95639](https://github.com/freeCodeCamp/artemis/commit/7d95639817a8bbd0505bde5b3ed3e7c86952e89e))
* **boot:** wire gc closures + policy + pg.Repo stores (prod R2 layout) ([ded5b80](https://github.com/freeCodeCamp/artemis/commit/ded5b80cb5d2df79bb5fb37a4fde8fe26246c9c6))
* **config:** add DATABASE_URL, HATCHET_*, CLEANUP_* with grace&gt;=ttl validation ([b6c1a08](https://github.com/freeCodeCamp/artemis/commit/b6c1a088bfb18b125380b79bd2660f4d8ccbd03d))
* **deploy:** write _artemis_meta.json marker on finalize ([b9cd06f](https://github.com/freeCodeCamp/artemis/commit/b9cd06f9b80a80eecabc5f50677310f375b9a20c))
* **gc:** add gc-site workflow (retain, TOCTOU re-check, tombstone-move, dry-run) ([52138cc](https://github.com/freeCodeCamp/artemis/commit/52138cc8b8cf733d14494d83e15ef48fe52ca7a2))
* **gc:** add prometheus metrics + slog reporting for GC workflows ([61315ac](https://github.com/freeCodeCamp/artemis/commit/61315ac04e5100469db16ac1bfc67a2d8d2ae22e))
* **gc:** add pure retain predicate (alias/keepN/grace/retention/serve-cache) ([7be6dc6](https://github.com/freeCodeCamp/artemis/commit/7be6dc670d6c09ba0c2cd3b676a9e691e8ffcfb4))
* **gc:** add reconcile-slice drift audit (orphan tombstone, reindex, PG prune) ([f4f9786](https://github.com/freeCodeCamp/artemis/commit/f4f97864e7b2d5538f4207872d10f433b75f41c2))
* **gc:** add site GC planner with blast-cap abort ([c211aeb](https://github.com/freeCodeCamp/artemis/commit/c211aebe2381dd8f3700e70cade0e229012feb0c))
* **gc:** add tombstone-purge workflow (2-phase reclaim past recovery window) ([882ae27](https://github.com/freeCodeCamp/artemis/commit/882ae27b1136a3c60de698d53a85ba3914e458ff))
* **handler:** add manual deploy-delete endpoint (409 if aliased, else tombstone) ([e3d19e1](https://github.com/freeCodeCamp/artemis/commit/e3d19e1f92f8a963facd0a355dbede3cf11365eb))
* **handler:** add site-purge (?purge=true cascade tombstone) ([ef3bc4b](https://github.com/freeCodeCamp/artemis/commit/ef3bc4bc371c27a227b47d8351abf9b20025e309))
* **hatchet:** adapter implementing worker.Engine + worker.Publisher ([9714509](https://github.com/freeCodeCamp/artemis/commit/971450970db5ecc705cd470e155106ae7dba98f0))
* **metrics:** expose worker run + relay counters at /metrics ([c3830a9](https://github.com/freeCodeCamp/artemis/commit/c3830a981d60124af65beb0d051b7c8eba4f304e))
* **observability:** capture outbox-enqueue failures to Sentry ([bd002f2](https://github.com/freeCodeCamp/artemis/commit/bd002f2ad89322af77bd78ff8c8317a4e4b31c64))
* **pg:** add alias CAS for last-writer-safe promote/rollback (no lost update) ([dc392ff](https://github.com/freeCodeCamp/artemis/commit/dc392ff826a39620799e29ea1d913d4c38ee3689))
* **pg:** add atomic finalize saga (deploy+alias+outbox in one tx) ([d8a1653](https://github.com/freeCodeCamp/artemis/commit/d8a16537e3010f3108e0304f60137b97c30fe0e8))
* **pg:** add deploy/alias/tombstone repo + one-time R2-&gt;PG backfill ([8286666](https://github.com/freeCodeCamp/artemis/commit/828666647a068d3e08ab533a5db446ac6755a2b6))
* **pg:** add Postgres layer with embedded schema migrations ([fd1f4bf](https://github.com/freeCodeCamp/artemis/commit/fd1f4bf750700b1262a7a2612c781c31cb1bd2ba))
* **pg:** add Postgres-backed repo-request queue (partial-index name claim, CAS transitions) ([c6a3827](https://github.com/freeCodeCamp/artemis/commit/c6a3827fe6c065ab2c5ad35ede649d8593687a6f))
* **pg:** add Postgres-backed site registry store (Valkey cache-front via OnChange) ([6807518](https://github.com/freeCodeCamp/artemis/commit/6807518426f49646ba01b89e0a7068e52c6f9086))
* **pg:** add transactional outbox + emit site.changed on finalize/promote/rollback ([7988ba0](https://github.com/freeCodeCamp/artemis/commit/7988ba0e4e91b8ce566730a038bc50669d9250c6))
* **r2:** add DeleteObject + paginated batch DeletePrefix ([53e14fe](https://github.com/freeCodeCamp/artemis/commit/53e14fe6cca802a6be6423b19a0c57d83a73de39))
* **r2:** add ListSites (top-level delimiter, _* excluded) ([1698048](https://github.com/freeCodeCamp/artemis/commit/169804879d4463c6037eec5f875dbe8ea7cda8ab))
* **r2:** add MovePrefix (copy+delete) for tombstone moves ([b1f248d](https://github.com/freeCodeCamp/artemis/commit/b1f248db128ce4d18436a8073160bcc3ce810025))
* **readyz:** PG-degraded probe semantics (R6) ([d075130](https://github.com/freeCodeCamp/artemis/commit/d07513089c0ee93b29d6d2c15f21eeda3ada6777))
* **registry:** cut registry SoT to pg.RegistryStore + valkey cache-front ([a40efe5](https://github.com/freeCodeCamp/artemis/commit/a40efe512ff169d27eb8243f8b1e5c6476fd03ab))
* **registry:** one-shot Valkey-to-PG import on boot when empty ([11b9be8](https://github.com/freeCodeCamp/artemis/commit/11b9be861cd86999ee32078c5af047c317e9b0fb))
* **teamcache:** add Valkey-backed shared GitHub team-membership cache ([a166a35](https://github.com/freeCodeCamp/artemis/commit/a166a354d4d3e220e2b1bd95b6e7741e24e10bd1))
* **worker:** add engine-agnostic durable workflow runtime (concurrency key=site) ([ade56a4](https://github.com/freeCodeCamp/artemis/commit/ade56a4315986b100ede9cbbb9f33bbaddfc2f5a))
* **worker:** add event/cron triggers to WorkflowDef + Hatchet adapter ([d7f68b4](https://github.com/freeCodeCamp/artemis/commit/d7f68b4c363e8aa6707d53c9ed7048e23a431bf1))
* **worker:** add outbox relay to publisher (at-least-once, order-preserving) ([e67ee1c](https://github.com/freeCodeCamp/artemis/commit/e67ee1c7a2b045ba432511da80232c70fc318fe7))
* **worker:** add per-site debouncer for site.changed gc-site triggers ([060a98d](https://github.com/freeCodeCamp/artemis/commit/060a98dd826b8bf899f9d4ef6a4ca794637e45ce))
* **worker:** add queue/DLQ/workflow metrics + reconcile drift counters ([26032dc](https://github.com/freeCodeCamp/artemis/commit/26032dccc5746ab68990b187946948f5e8a2e387))
* **worker:** register finalize/promote/rollback as durable workflows (key=site) ([701ac31](https://github.com/freeCodeCamp/artemis/commit/701ac31b7c418b42555bfb92a7a91b5816ac8838))


### Bug Fixes

* **auth:** surface io.ReadAll + parse errors on GitHub OK path ([ae9ebf8](https://github.com/freeCodeCamp/artemis/commit/ae9ebf8014805647d01bdb19edf1ddd14f8efff5))
* **auth:** tolerate durable team cache write fail ([531f491](https://github.com/freeCodeCamp/artemis/commit/531f491d77998e7e1231826a7545ab1bc9d74d05))
* **backfill:** honor configurable ALIAS_*_KEY_FORMAT instead of hardcoded keys ([10d0074](https://github.com/freeCodeCamp/artemis/commit/10d0074c02aeeb466bc65a7842335dd6e37e5621))
* **backfill:** revert alias-key templating; read R2-dir-relative &lt;dir&gt;/&lt;mode&gt; (B3 was a false positive) ([81e3ccd](https://github.com/freeCodeCamp/artemis/commit/81e3ccd74d8670417daca6e4f25ad5723b16f53f))
* **compose:** boot smoke stack past R11 via loopback fakegithub + pg ([f4c05a5](https://github.com/freeCodeCamp/artemis/commit/f4c05a5f4ffcb0c8ccb2dd1a0745671551cf4af5))
* **config:** reject whitespace-only authz team ([939559a](https://github.com/freeCodeCamp/artemis/commit/939559a1414c7bc847738e6d95404d513967ec19))
* **config:** validate GH_API_BASE; reject cleartext-remote + malformed bases ([8bc0170](https://github.com/freeCodeCamp/artemis/commit/8bc01704b5419449c6bc8fe684fa70cd1e6aa316))
* **gc:** never tombstone an alias-pinned deploy in reconcile (V1) ([935df49](https://github.com/freeCodeCamp/artemis/commit/935df4996c8fd433ab52f51988925f7dd220718d))
* **gc:** re-read aliases before reconcile tombstone to close TOCTOU (V1) ([130638c](https://github.com/freeCodeCamp/artemis/commit/130638caf8bbb15d11825f025207df5a4a36e735))
* **handler:** detach emit from request ctx ([069ab55](https://github.com/freeCodeCamp/artemis/commit/069ab55df24f3c671e68bb194c0df2a4cdf4253b))
* **handler:** purge R2 before registry delete ([b94f581](https://github.com/freeCodeCamp/artemis/commit/b94f581c92f592765ad3562794dfc49f42f60029))
* **metrics:** expose go and process collectors ([cb87f3e](https://github.com/freeCodeCamp/artemis/commit/cb87f3edc3acada4a05fb45c32807363aad0e042))
* **pg:** count only inserted rows in import ([a5db15b](https://github.com/freeCodeCamp/artemis/commit/a5db15be5efe445c8599a7a3387626018a58a97e))
* **pg:** panic on crypto/rand failure in repo request id gen ([fd23e57](https://github.com/freeCodeCamp/artemis/commit/fd23e576a776f8c127014893104e912eef395fc6))
* **pg:** rebuild outbox_unpublished_idx on id to match fetch order ([f9f0d10](https://github.com/freeCodeCamp/artemis/commit/f9f0d109710ba7bb8954219cdbc4a42a97b033c9))
* **pg:** return DB-read value as current from SetAliasCAS ([2170962](https://github.com/freeCodeCamp/artemis/commit/217096209b9a0f90f39ba9a61a3afa078bcfa75d))
* **pg:** unlock advisory locks on fresh ctx ([71489c8](https://github.com/freeCodeCamp/artemis/commit/71489c8dc3869109f1a07071400a756e87153bb7))
* **r2:** URL-encode MovePrefix copy-source for space/non-ASCII keys (V5) ([2dff82e](https://github.com/freeCodeCamp/artemis/commit/2dff82efa8251beb1c5840a38e5a15d9c57fae30))
* **scripts:** fail fast on pg readiness timeout ([535c721](https://github.com/freeCodeCamp/artemis/commit/535c7218f24731eb1ac669e75af2fb0dbcc4c1ae))
* **worker:** close debounce timer capture race ([70120ff](https://github.com/freeCodeCamp/artemis/commit/70120fff67a5f074bc4ffb74e776a39ee6e314a5))
* **worker:** guard debounce callback against stale timer race ([4587b39](https://github.com/freeCodeCamp/artemis/commit/4587b390beba08c440385fafb26330828dd13de8))
* **worker:** surface mark-published error on relay publish failure (errcheck) ([2792bb7](https://github.com/freeCodeCamp/artemis/commit/2792bb7d69d2506d04502b7dd7e06783acdb4c0b))


### Miscellaneous Chores

* release 1.0.0 ([3ca3271](https://github.com/freeCodeCamp/artemis/commit/3ca3271cc8213a0e34f229e664167ee53aee6ef0))

## [0.8.0](https://github.com/freeCodeCamp/artemis/compare/v0.7.1...v0.8.0) (2026-06-02)


### Features

* **repo:** delete endpoint + stale-claim reconcile ([c3a7271](https://github.com/freeCodeCamp/artemis/commit/c3a72711a08ce270953f41395d57158187273ca0))


### Bug Fixes

* **repo:** correct delete claim-release + reconcile ([0259bdb](https://github.com/freeCodeCamp/artemis/commit/0259bdb54586b22117754b83edb89031f9e7eb70))
* **repo:** log reconcile probe failure ([493a60c](https://github.com/freeCodeCamp/artemis/commit/493a60c7f36d65ef5ef1bb41128b654161db8a05))

## [0.7.1](https://github.com/freeCodeCamp/artemis/compare/v0.7.0...v0.7.1) (2026-06-01)


### Bug Fixes

* **handler:** cap json request body sizes ([2859d1f](https://github.com/freeCodeCamp/artemis/commit/2859d1fab3788f6e098b36f098384c853e57d6fd))
* **handler:** raise readyz probe timeout to 5s ([7490776](https://github.com/freeCodeCamp/artemis/commit/749077638779fbb390db0f99e7897472430bc5b2))
* **handler:** run readyz probes concurrently ([238c51e](https://github.com/freeCodeCamp/artemis/commit/238c51e45ab679e99ce4287985666800e964cbcb))
* **handler:** validate rollback target deploy id ([c5bd8c1](https://github.com/freeCodeCamp/artemis/commit/c5bd8c1edc6fe0c3149ba2e623327f6d674224fa))

## [0.7.0](https://github.com/freeCodeCamp/artemis/compare/v0.6.1...v0.7.0) (2026-06-01)


### Features

* **preflight:** add Apollo-11 App credential smoke command ([5cf4287](https://github.com/freeCodeCamp/artemis/commit/5cf4287eed708f50075ee2267e6f86a0c7714198))

## [0.6.1](https://github.com/freeCodeCamp/artemis/compare/v0.6.0...v0.6.1) (2026-05-31)

### Bug Fixes

- **config:** reject non-numeric GH App ids at boot ([a6635c2](https://github.com/freeCodeCamp/artemis/commit/a6635c233f42f1d5edba12b9ab7506918bab599f))

## [0.6.0](https://github.com/freeCodeCamp/artemis/compare/v0.5.0...v0.6.0) (2026-05-30)

### Features

- **githubapp:** surface GitHub message across remaining error paths ([5412053](https://github.com/freeCodeCamp/artemis/commit/5412053ead3f570d28f6ed3746f04a90e46e59a5))
- **githubapp:** surface GitHub message in install-token error ([ae39198](https://github.com/freeCodeCamp/artemis/commit/ae3919815776c4cddb727b57c8f94dd75a52ee20))
- **handler:** structured outcome logs across repo/site/deploy endpoints ([0304df7](https://github.com/freeCodeCamp/artemis/commit/0304df7dcee038a92fa5bf71413713c74ce12b1a))
- **handler:** surface error code on every request access log line ([703b102](https://github.com/freeCodeCamp/artemis/commit/703b102d17328adf65db8f16898965fe1430d4a9))
- **repo:** bound description length server-side ([02036d8](https://github.com/freeCodeCamp/artemis/commit/02036d808ff963337648652b16ee94c883832564))

### Bug Fixes

- **githubapp:** cap App JWT exp at now+540s under GitHub 600s limit ([afca8af](https://github.com/freeCodeCamp/artemis/commit/afca8afc2690c746d1ada379d71cd21f77e2c878))
- **repo:** create repo on durable context, surviving client disconnect ([f02bf42](https://github.com/freeCodeCamp/artemis/commit/f02bf4245d536df599fd21be59d5b8eb104cea74))
- **repo:** leave row approved on transient error during resume ([fc46e35](https://github.com/freeCodeCamp/artemis/commit/fc46e35b8d567e4b814703ecb9f8e76b5b70e975))

## [0.5.0](https://github.com/freeCodeCamp/artemis/compare/v0.4.0...v0.5.0) (2026-05-30)

### Features

- **observability:** add Sentry monitoring ([83b5665](https://github.com/freeCodeCamp/artemis/commit/83b5665c5aa2e56b467a505dff28462868fae749))

## [0.4.0](https://github.com/freeCodeCamp/artemis/compare/v0.3.0...v0.4.0) (2026-05-30)

### Features

- **config:** add repo-creation feature config ([a5405db](https://github.com/freeCodeCamp/artemis/commit/a5405db7b8afcfc997ad78f4faa57718a1db85fb))
- **config:** default approve team to apollo-11-approvers ([46a26b1](https://github.com/freeCodeCamp/artemis/commit/46a26b126b8a9c71def615e805eed33e8fd94294))
- **githubapp:** add repo-creation REST client ([af8fea1](https://github.com/freeCodeCamp/artemis/commit/af8fea1596b2de9387e6dec1a37dae7a380f57cc))
- **githubapp:** mint Apollo-11 App JWT (RS256) ([4da60fb](https://github.com/freeCodeCamp/artemis/commit/4da60fb5b46e1df5d33f1247c708f512115f558b))
- **handler:** add repo-request endpoints ([68c7fe8](https://github.com/freeCodeCamp/artemis/commit/68c7fe8a60c09659490029c5d5e4e945f118142a))
- **reporequest:** add repo-request domain types ([bc0519a](https://github.com/freeCodeCamp/artemis/commit/bc0519a58b40d8b1b389a0532f34808f6bb1e03d))
- **reporequest:** add valkey-backed request queue store ([27321bb](https://github.com/freeCodeCamp/artemis/commit/27321bbcb34f7fbdcd07afe6fca8a8838a75eb5d))
- **server:** wire repo-request routes and app client ([6fad4ae](https://github.com/freeCodeCamp/artemis/commit/6fad4ae167ba3ccbbe5b8af3cfa56cfa19cc2a45))

### Bug Fixes

- **repo:** 400 on malformed reject body ([8b6ab04](https://github.com/freeCodeCamp/artemis/commit/8b6ab04edc75c55c9338e1790ca3c49939039c3c))
- **repo:** keep internal GitHub errors out of approve body ([8af2d6c](https://github.com/freeCodeCamp/artemis/commit/8af2d6cc263c7714ea6d43649e20f426d33e6fd6))
- **repo:** recover approvals stranded after repo creation ([1165559](https://github.com/freeCodeCamp/artemis/commit/11655591671ba636e1307f81a157d901078458d2))
- **reporequest:** case-insensitive name dedupe + nil guard ([685c035](https://github.com/freeCodeCamp/artemis/commit/685c0350e4fb4f5735ee0155e628c59c91c76d0a))

### Performance Improvements

- **repo:** cache accessible template list with TTL ([3a8461a](https://github.com/freeCodeCamp/artemis/commit/3a8461a591c169df19e936d414f8afa0cd8a28d5))

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
