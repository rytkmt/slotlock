# slotlock

同一 scope 内で key ごとに安定した整数スロット（1..max）を割り当て、割り当て中の
同一 `(scope, key)` の同時実行を禁止する小さな CLI。

並列テストなどで「プロセス（呼び出し元）ごとに重複しない番号」を安定して得たいときに使う。
例: rspec の `TEST_ENV_NUMBER` を herdr ワークスペースごとに固定割り当てし、
同じ番号のテストDBを使い回す。

## 仕組み（2層）

1. **割り当て（永続 / SQLite）**: `(scope, key) -> slot_number`。同じ `(scope, key)` は
   常に同じ番号を返す。
2. **同時実行排他（一時 / flock）**: 同じ `(scope, key)` を同時に2本走らせない。
   2本目は待たずに即エラー。flock はプロセス消滅で自動解放されるため stale lock が残らない。

スロットは TTL（デフォルト24h）で遅延回収する。新しい key が空きを要求したとき、
空きが無ければ最終利用が TTL より古い割り当ての番号を再利用する。

## ビルド

```sh
CGO_ENABLED=0 go build -o slotlock .
```

純Go（`modernc.org/sqlite`）なので C コンパイラ不要。`CGO_ENABLED=0` を付ける。

## 使い方

```sh
slotlock run --scope rspec --key "$HERDR_WORKSPACE_ID" --max 8 -- \
  bash -c 'echo slot=$SLOT_NUMBER'

slotlock list --scope rspec
slotlock release --scope rspec --key w7T
slotlock help     # AI 向けの詳細解説つき
```

DB ファイルは `$SLOTLOCK_DB` → `$XDG_DATA_HOME/slotlock/slotlock.db` →
`~/.local/share/slotlock/slotlock.db` の順で解決する。

## herdr ラッパー

`scripts/slotlock-herdr` は key を `$HERDR_WORKSPACE_ID` で自動的に埋める薄い
ラッパー。slotlock 本体は herdr を知らず、このラッパーだけが herdr に依存する。

```sh
slotlock-herdr --scope rspec --max 8 -- <command...>
# 内部的に: slotlock run --key "$HERDR_WORKSPACE_ID" --scope rspec --max 8 -- <command...>
```
