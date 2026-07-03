# Changelog

## [1.3.0](https://github.com/garethgeorge/fileferry/compare/v1.2.0...v1.3.0) (2026-07-03)


### Features

* allow seeking in encrypted archives ([59d4a7e](https://github.com/garethgeorge/fileferry/commit/59d4a7ee84e396ce8148b4f28ee477d3dcacb407))
* new ferryupload binary for easy uploads, link shortening, various refactors ([412da64](https://github.com/garethgeorge/fileferry/commit/412da64def3b7b0bf962adcb2899d14d3758dba4))


### Bug Fixes

* support uploading files from clipboard ([1c2475c](https://github.com/garethgeorge/fileferry/commit/1c2475c06d49bfaed7ebba2baa600d0899e1ce0e))

## [1.2.0](https://github.com/garethgeorge/fileferry/compare/v1.1.0...v1.2.0) (2026-07-03)


### Features

* API keys, better ui, simplified API ([a864eb7](https://github.com/garethgeorge/fileferry/commit/a864eb71a3ed5547de256612cd6c0acc3611692d))


### Bug Fixes

* sort behavior uses atime to sort within a day ([71b4146](https://github.com/garethgeorge/fileferry/commit/71b4146044d19863c9df520c6f54216e06dd3a91))

## [1.1.0](https://github.com/garethgeorge/fileferry/compare/v1.0.0...v1.1.0) (2026-07-02)


### Features

* support environment variable configuration and update README.md ([68d3b9a](https://github.com/garethgeorge/fileferry/commit/68d3b9a852a81fdf57d7ff3098f956a7940e11d8))


### Bug Fixes

* simplify media serving, makes it easier to embed images / videos ([7ed8b88](https://github.com/garethgeorge/fileferry/commit/7ed8b88892f12422469d61d212c754977a186af3))

## 1.0.0 (2026-07-02)


### ⚠ BREAKING CHANGES

* change ID scheme

### Features

* change ID scheme ([15df78a](https://github.com/garethgeorge/fileferry/commit/15df78ab0b9d6fba120bf4aa8221962c80d7f41e))
* encryption support ([6c7cb68](https://github.com/garethgeorge/fileferry/commit/6c7cb68f944dcf6588227d60237554f58e0d71d5))
* initial commit ([2bf86fc](https://github.com/garethgeorge/fileferry/commit/2bf86fc413ef8cbbd7280c795c2e14f9d156d3f0))
* more content types and ui refinements ([32cc29b](https://github.com/garethgeorge/fileferry/commit/32cc29bd645ccaa5e50cad2f3a00c0c927b5dd41))


### Bug Fixes

* avoid expensive scan to remove inprogress uploads on startup ([40f945e](https://github.com/garethgeorge/fileferry/commit/40f945eae95d9325122ce0881a5ac4ddb306323b))
