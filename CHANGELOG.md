# Changelog

## [0.3.0](https://github.com/abemedia/go-cabinet/compare/v0.2.0...v0.3.0) (2026-10-22)


### ⚠ BREAKING CHANGES

* Custom codecs implement the Encoder and Decoder interfaces instead of wrapping io.Writer and io.Reader. mszip.NewWriter and mszip.NewReader are replaced by mszip.NewEncoder and mszip.NewDecoder. SetCompression rejects an invalid or unregistered method immediately instead of Close failing later.
* MSZip is now MSZIP and Compression.String returns MSZIP instead of MS-ZIP.

### Features

* add compression settings and block-based codecs ([#25](https://github.com/abemedia/go-cabinet/issues/25)) ([bbe9967](https://github.com/abemedia/go-cabinet/commit/bbe996737d11cf4e4647493bf9e28abe5051f8ba))


### Code Refactoring

* rename MSZip to MSZIP ([#23](https://github.com/abemedia/go-cabinet/issues/23)) ([4db1b9c](https://github.com/abemedia/go-cabinet/commit/4db1b9c3f6a10ed50fc1b9f3d5580cda7686221b))

## [0.2.0](https://github.com/abemedia/go-cabinet/compare/v0.1.0...v0.2.0) (2026-05-05)


### Features

* expose FileHeader via fs.FileInfo.Sys() and preserve attributes on AddFS roundtrip ([#6](https://github.com/abemedia/go-cabinet/issues/6)) ([3814efd](https://github.com/abemedia/go-cabinet/commit/3814efd89139434c0501666ae1538e9f44aec6ef))

## [0.1.0](https://github.com/abemedia/go-cabinet/compare/v0.0.1...v0.1.0) (2026-04-03)


### Features

* implement cabinet file reader/writer & MS-ZIP compression ([#1](https://github.com/abemedia/go-cabinet/issues/1)) ([37b2a63](https://github.com/abemedia/go-cabinet/commit/37b2a638c2e76c115af01bc9698814b12b62c26b))
