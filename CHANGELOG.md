# Changelog

## [1.0.2](https://github.com/BlitterAmp/BlitterServer/compare/v1.0.1...v1.0.2) (2026-07-15)


### Bug Fixes

* dispatch server releases across repositories ([#46](https://github.com/BlitterAmp/BlitterServer/issues/46)) ([be09b92](https://github.com/BlitterAmp/BlitterServer/commit/be09b926a5dd65db3ed423f46090dc96b112fa62))

## [1.0.1](https://github.com/BlitterAmp/BlitterServer/compare/v1.0.0...v1.0.1) (2026-07-15)


### Bug Fixes

* enforce stable v1 API compatibility ([#44](https://github.com/BlitterAmp/BlitterServer/issues/44)) ([e1a054f](https://github.com/BlitterAmp/BlitterServer/commit/e1a054f18604cee109ba48f4faf722d21e1ee50c))

## 1.0.0 (2026-07-15)


### ⚠ BREAKING CHANGES

* **web:** stop committing the built admin console; build via make/CI instead ([#14](https://github.com/BlitterAmp/BlitterServer/issues/14))

### Features

* add canonical artist credits ([#23](https://github.com/BlitterAmp/BlitterServer/issues/23)) ([ebe9e55](https://github.com/BlitterAmp/BlitterServer/commit/ebe9e559c26e4fae85c93b0e3f2374d1d2f2d6f3))
* add personalized mixes and server releases ([#42](https://github.com/BlitterAmp/BlitterServer/issues/42)) ([58db29d](https://github.com/BlitterAmp/BlitterServer/commit/58db29db1145fc648726433eeaff6631f36f16e7))
* admin realm, device pairing, profiles, and settings — 32 live contract ops ([#7](https://github.com/BlitterAmp/BlitterServer/issues/7)) ([cba8597](https://github.com/BlitterAmp/BlitterServer/commit/cba85970d1a9334e909a7d2ae808847d0bce75e5))
* **api:** session-2 contract — SSE events, loves, cursor pagination, social, QR pairing ([#4](https://github.com/BlitterAmp/BlitterServer/issues/4)) ([094f6d2](https://github.com/BlitterAmp/BlitterServer/commit/094f6d290e5fe3d12c96b00452895eb5821486cc))
* **api:** spec iteration — artifact art ids + household profiles ([#2](https://github.com/BlitterAmp/BlitterServer/issues/2)) ([8bcb190](https://github.com/BlitterAmp/BlitterServer/commit/8bcb190504e139ce8665c803df3c881934686316))
* artifact pipeline — ffmpeg transcodes, LRU cache, exact-length downloads ([#11](https://github.com/BlitterAmp/BlitterServer/issues/11)) ([88939d9](https://github.com/BlitterAmp/BlitterServer/commit/88939d9f787f8bf283bb325c14d41a1d88f49737))
* bound art resize cache and guard schema drift ([#30](https://github.com/BlitterAmp/BlitterServer/issues/30)) ([40eaf34](https://github.com/BlitterAmp/BlitterServer/commit/40eaf342766be23b2d58088957bd69237cd867b1))
* cache provider responses outside the library database ([#27](https://github.com/BlitterAmp/BlitterServer/issues/27)) ([2a7505a](https://github.com/BlitterAmp/BlitterServer/commit/2a7505ae1a34b035450402584aa3a97c6e8f0c61))
* complete last.fm and fanart integrations ([#17](https://github.com/BlitterAmp/BlitterServer/issues/17)) ([e6863a3](https://github.com/BlitterAmp/BlitterServer/commit/e6863a342c8318dec38706319542ec9bd4f0f6da))
* consolidate artists under canonical MusicBrainz identity ([#28](https://github.com/BlitterAmp/BlitterServer/issues/28)) ([eaf4590](https://github.com/BlitterAmp/BlitterServer/commit/eaf4590803db30735e2249506fa130e27a81809c))
* discovery, listen parties, and integration config — 95 of 99 contract ops live ([#12](https://github.com/BlitterAmp/BlitterServer/issues/12)) ([b398cd1](https://github.com/BlitterAmp/BlitterServer/commit/b398cd175751e37368eef903a6fa0d3f2e0d80ac))
* expose library activity in server status ([#39](https://github.com/BlitterAmp/BlitterServer/issues/39)) ([34c35a6](https://github.com/BlitterAmp/BlitterServer/commit/34c35a64933bb769a66421b776e3ac3263b5b97f))
* filesystem source, library index, browse + streaming — 52 live contract ops ([#9](https://github.com/BlitterAmp/BlitterServer/issues/9)) ([cf7b747](https://github.com/BlitterAmp/BlitterServer/commit/cf7b747b1e6e45d7b10f641fb28901ea8eaa0c55))
* improve artwork enrichment coverage ([#36](https://github.com/BlitterAmp/BlitterServer/issues/36)) ([98c5130](https://github.com/BlitterAmp/BlitterServer/commit/98c5130fe7da8415a5b2833aa6b8fae7edea6373))
* **library:** catalog delta-sync — change tracking + GET /v1/changes ([#15](https://github.com/BlitterAmp/BlitterServer/issues/15)) ([d60f7b7](https://github.com/BlitterAmp/BlitterServer/commit/d60f7b70d7ab4041b57d099e395dc19e09849fde))
* **library:** fill missing album/artist art from MusicBrainz/CAA/last.fm/fanart.tv ([#16](https://github.com/BlitterAmp/BlitterServer/issues/16)) ([91afdae](https://github.com/BlitterAmp/BlitterServer/commit/91afdae66d801eb8f7ccbd9c2237de594524635c))
* per-profile data + SSE — playlists, loves, ratings, playback, presence, recommendations ([#10](https://github.com/BlitterAmp/BlitterServer/issues/10)) ([239e266](https://github.com/BlitterAmp/BlitterServer/commit/239e266a7deb5d6f8cc57bbd03457e380007965a))
* reconcile library identity and genres ([#40](https://github.com/BlitterAmp/BlitterServer/issues/40)) ([0e0d16b](https://github.com/BlitterAmp/BlitterServer/commit/0e0d16b8bcb01c50b07116bae306aaec44a8d576))
* resolve canonical MusicBrainz identity ([#25](https://github.com/BlitterAmp/BlitterServer/issues/25)) ([52dca30](https://github.com/BlitterAmp/BlitterServer/commit/52dca30a7067a3741d4531dbed0487d92b6134f5))
* retry missing artwork dynamically ([#22](https://github.com/BlitterAmp/BlitterServer/issues/22)) ([d27afbc](https://github.com/BlitterAmp/BlitterServer/commit/d27afbc45f4143096fb1347401ea86f9ce507de2))
* scaffold Go service serving the API contract in a docs viewer ([#1](https://github.com/BlitterAmp/BlitterServer/issues/1)) ([1109c02](https://github.com/BlitterAmp/BlitterServer/commit/1109c028564cc9731fb2b76a36aad46d9543ef1e))
* server foundation + rename to BlitterServer ([#5](https://github.com/BlitterAmp/BlitterServer/issues/5)) ([6e37fcf](https://github.com/BlitterAmp/BlitterServer/commit/6e37fcf157ee4cd568c1ba8e6710cbb59ccb5d94))
* tier artwork retry scheduling and pace providers ([#29](https://github.com/BlitterAmp/BlitterServer/issues/29)) ([032874b](https://github.com/BlitterAmp/BlitterServer/commit/032874b05a808c4a329a5ef99347b88ed72c87d8))
* **web:** embedded admin console — Svelte + DaisyUI SPA served at /admin/ ([#13](https://github.com/BlitterAmp/BlitterServer/issues/13)) ([0eac450](https://github.com/BlitterAmp/BlitterServer/commit/0eac4506789e55ee759d3a8d6d0b65b634bc2b0e))


### Bug Fixes

* accept opaque last.fm callback tokens ([#21](https://github.com/BlitterAmp/BlitterServer/issues/21)) ([1321fa7](https://github.com/BlitterAmp/BlitterServer/commit/1321fa711ae359e13e51f76d9c2abad40012d359))
* apply edition-consensus identity and normalize search titles ([#33](https://github.com/BlitterAmp/BlitterServer/issues/33)) ([c5e392b](https://github.com/BlitterAmp/BlitterServer/commit/c5e392bf1d7fcfe4b406dff9caf40fc4ec01199d))
* break edition ties with structural evidence ([#34](https://github.com/BlitterAmp/BlitterServer/issues/34)) ([5dfb944](https://github.com/BlitterAmp/BlitterServer/commit/5dfb9446fc413163ec3da7efd1caad7076636faf))
* cache filesystem metadata and expose scan progress ([#38](https://github.com/BlitterAmp/BlitterServer/issues/38)) ([4c1d0af](https://github.com/BlitterAmp/BlitterServer/commit/4c1d0af96b19a16545ab97bc6d2d5c167f5f6e42))
* carry last.fm callback state in the path ([#31](https://github.com/BlitterAmp/BlitterServer/issues/31)) ([894b32e](https://github.com/BlitterAmp/BlitterServer/commit/894b32e42135c07c1b156f17aafbd942fd10dad2))
* consolidate canonical artist resources ([#37](https://github.com/BlitterAmp/BlitterServer/issues/37)) ([7672f34](https://github.com/BlitterAmp/BlitterServer/commit/7672f342214983ebad84904984d342cae07dfb1d))
* default event streams to live-only ([#35](https://github.com/BlitterAmp/BlitterServer/issues/35)) ([049dfec](https://github.com/BlitterAmp/BlitterServer/commit/049dfec8c41d9ba6a94ee57d9360d8d1302760b5))
* expose album art as artist fallback ([#20](https://github.com/BlitterAmp/BlitterServer/issues/20)) ([d6caca9](https://github.com/BlitterAmp/BlitterServer/commit/d6caca95d21eb9b141e2917bf388a168bf813ffa))
* fetch artwork before and during identity drains ([#32](https://github.com/BlitterAmp/BlitterServer/issues/32)) ([c2aca9d](https://github.com/BlitterAmp/BlitterServer/commit/c2aca9d62544906d6afdb6b59d428ce4814a232a))
* keep embedded admin directory ([#24](https://github.com/BlitterAmp/BlitterServer/issues/24)) ([3f40c35](https://github.com/BlitterAmp/BlitterServer/commit/3f40c353fd24c38da97accf5a66f0472beb4b1fe))
* normalize malformed album title prefixes ([#41](https://github.com/BlitterAmp/BlitterServer/issues/41)) ([300df26](https://github.com/BlitterAmp/BlitterServer/commit/300df2647d860080abcb8d56e74e6768e8bc391d))
* retain embedded admin placeholder ([#26](https://github.com/BlitterAmp/BlitterServer/issues/26)) ([b6af262](https://github.com/BlitterAmp/BlitterServer/commit/b6af262d1626ccfdedd851edc7a2dd15dc55b529))
* retrigger art enrichment after credential saves ([#19](https://github.com/BlitterAmp/BlitterServer/issues/19)) ([169bdb4](https://github.com/BlitterAmp/BlitterServer/commit/169bdb4c08f193472d85c62d2dec2c23436eba95))


### Miscellaneous Chores

* **web:** stop committing the built admin console; build via make/CI instead ([#14](https://github.com/BlitterAmp/BlitterServer/issues/14)) ([ff36132](https://github.com/BlitterAmp/BlitterServer/commit/ff361329eb789289770518caeb76dc66e14fae41))
