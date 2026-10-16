# Changelog

このファイルは slotlock の主な変更を記録します。
形式は [Keep a Changelog](https://keepachangelog.com/ja/1.1.0/) に、
バージョニングは [Semantic Versioning](https://semver.org/lang/ja/) に従います。

## [Unreleased]

### Changed

- README のリリース手順のタグ例を `<version>` プレースホルダ表記に変更

## [0.3.0] - 2026-10-16

### Added

- `acquire` サブコマンドを追加。`(scope, key)` にスロット番号を確保（永続割り当て）して
  番号を1行出力する。`run` と違い flock は掴まずコマンドも実行しない。`get`（参照のみ）と
  違い、未割り当てなら新規に確保する

## [0.2.1] - 2026-10-15

### Changed

- `slotlock-herdr` が任意のサブコマンドを受け付けるようになった。`list` / `help` は
  key を使わないため `--key` の自動補完を行わない

## [0.2.0] - 2026-10-15

### Added

- `get` サブコマンドを追加。`(scope, key)` に割り当て済みの番号を1行出力する（参照のみ）

### Changed

- README を初見の読み手向けに全面的に書き直し

## [0.1.0] - 2026-10-15

### Added

- 初版。`run` でスロットを確保し `SLOT_NUMBER` を入れてコマンドを実行する
- 割り当ての永続化（SQLite）と同時実行の排他（flock）の2層構造
- TTL による古い割り当ての遅延再利用
- `list` / `release` サブコマンド
- `slotlock-herdr` ラッパースクリプト
- `v*` タグ push でバイナリをビルドし Release に添付する GitHub Actions ワークフロー

[Unreleased]: https://github.com/rytkmt/slotlock/compare/v0.3.0...HEAD
[0.3.0]: https://github.com/rytkmt/slotlock/compare/v0.2.1...v0.3.0
[0.2.1]: https://github.com/rytkmt/slotlock/compare/v0.2.0...v0.2.1
[0.2.0]: https://github.com/rytkmt/slotlock/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/rytkmt/slotlock/releases/tag/v0.1.0
