# slotlock

**複数のプロセスに、重複しない「番号」を安定して割り振るための小さな CLI。**

## 何のためのツール？

並列でテストやジョブを走らせるとき、「プロセスごとに別々の番号を使いたい」ことがあります。

典型例は **並列 rspec** です。テストを同時に複数走らせると、全部が同じテスト用DBを
使ってしまい壊れます。これを避けるには「プロセスAはDB番号2、プロセスBはDB番号3…」と
**かぶらない番号**を配る必要があります（rspec では `TEST_ENV_NUMBER` がこの番号）。

slotlock はこの「番号配り」を担当します。やってくれることは2つだけです。

1. **番号の割り当て** — 呼び出し元ごとに `1`〜`8` のような整数（スロット）を1つ渡す。
   同じ呼び出し元には毎回同じ番号を返すので、その番号に紐づく資源（テストDB など）を
   使い回せる。
2. **同時実行の禁止** — 同じ呼び出し元が同じ番号で二重に走るのを防ぐ（2本目はエラー）。

slotlock 自身はテストもDBも知りません。「番号を1つ確保して、あなたのコマンドを実行する」
だけの汎用ツールです。

## 用語

- **scope（スコープ）** — 番号を数える名前空間。scope が違えば番号は独立して数えられる
  （例: `rspec` と `jest` で別々に 1〜8 を使える）。
- **key（キー）** — scope の中で番号を所有する識別子。「誰にその番号を渡したか」。
  同じ key には常に同じ番号が返る。
- **slot（スロット）** — 割り当てられる整数そのもの（`--min`〜`--max` の範囲）。

## 基本の使い方

`slotlock run` は「スロットを1つ確保 → 環境変数 `SLOT_NUMBER` に入れて → `--` 以降の
コマンドを実行」します。番号はコマンドの実行中ずっとロックされ、コマンドが終わると
自動で解放されます（途中でクラッシュしても解放される）。

```sh
slotlock run --scope rspec --key myjob --min 2 --max 8 -- \
  bash -c 'echo "私の番号は $SLOT_NUMBER です"'
# => 私の番号は 2 です
```

ポイント: 番号は **`SLOT_NUMBER` 環境変数でコマンドに渡される**ので、コマンド側で
好きなように使います。上の `echo` は「番号を使う例」で、実運用では下記のように
テスト用の環境変数へ渡します。

### 並列 rspec の例

```sh
slotlock run --scope rspec --key "$MY_ID" --min 2 --max 8 -- \
  bash -c 'TEST_ENV_NUMBER=$SLOT_NUMBER bundle exec rspec'
```

別々の `--key` で同時に呼べば、それぞれ違う番号（2, 3, 4 …）を受け取り、別々の
テストDBを使って並列実行できます。同じ `--key` で二重に呼ぶと2本目はエラーになり、
DB の食い合いを防ぎます。

## コマンド一覧

| コマンド | 役割 |
| --- | --- |
| `slotlock run --scope S --key K [--min M] [--max N] -- CMD...` | スロットを確保し `SLOT_NUMBER` を入れて CMD を実行 |
| `slotlock acquire --scope S --key K [--min M] [--max N]` | スロットを確保（永続割り当て）して番号を1行出力。flock は掴まず CMD も実行しない |
| `slotlock get --scope S --key K` | いま (S,K) に割り当たっている番号を1行だけ出力（参照のみ） |
| `slotlock list [--scope S]` | 割り当て状況を一覧表示 |
| `slotlock release --scope S --key K` | (S,K) の割り当てを手動で解放 |
| `slotlock help` | 詳細ヘルプ（AI 向けの解説も含む） |

`get` は番号だけを標準出力に出すので、スクリプトから受け取れます（割り当てが
無ければ何も出さず exit 1）。

```sh
n=$(slotlock get --scope rspec --key myjob) && echo "現在の番号: $n"
```

`acquire` は `get` と似ていますが、**未割り当てなら新しく番号を確保して永続化する**点が
違います（`get` は参照のみで、無ければ exit 1）。`run` と違い flock は掴まず、コマンドも
実行しません。番号だけ先に決めておきたい（例: その番号のテストDBを事前に作っておく）
ときに使います。同時実行の排他は行わないので、二重起動を防ぎたい場合は `run` を使います。

```sh
n=$(slotlock acquire --scope rspec --key myjob --min 2 --max 8) && echo "確保した番号: $n"
```

## 番号の割り当てルール

- 範囲は `--min`（既定 `1`）〜`--max`（既定 `8`）。
  低い番号を予約したい場合は `--min` を上げる
  （例: `--min 2` で `1` を既定DB用に空けておく）。
- 同じ `(scope, key)` には**常に同じ番号**を返す（割り当ては SQLite に永続化される）。
- 新しい key が番号を要求したとき空きが無ければ、**最終利用から `--ttl`（既定 24h）
  以上経った古い割り当て**の番号を再利用する。誰も新規に要求しなければ番号は残り続ける
  （実害が出るのは枯渇したときだけなので、掃除は要求時にその場で行う）。
- どの番号も空いていなければエラー（exit 1）。

## 仕組み（2層）

内部では役割の違う2つの層を使っています。

1. **割り当て（永続 / SQLite）** — `(scope, key) -> slot_number` の対応を記録する。
   「どの key がどの番号か」を覚えるだけ。
2. **同時実行の排他（一時 / flock）** — 確保した番号のロックファイルを掴む。
   同じ番号で二重に走ろうとすると2本目は即エラー。flock はプロセスが消えると
   自動で解放されるので、クラッシュしても古いロックが残りません。

## インストール

[GitHub Release](https://github.com/rytkmt/slotlock/releases) に linux/amd64 の
`slotlock` バイナリと `slotlock-herdr` スクリプトを添付しています。public な
Release なので認証不要でダウンロードできます。

```sh
BASE=https://github.com/rytkmt/slotlock/releases/latest/download
curl -fsSL "$BASE/slotlock"        -o ~/.local/bin/slotlock
curl -fsSL "$BASE/slotlock-herdr"  -o ~/.local/bin/slotlock-herdr
chmod +x ~/.local/bin/slotlock ~/.local/bin/slotlock-herdr
```

`latest/download` は常に最新リリースへ解決されるので、新しいバージョンが出ても
この手順を書き換える必要はありません。バージョンを固定したい場合（CI など再現性が
必要なとき）は `latest` の代わりにタグを指定します。

```sh
VERSION=v0.3.0
BASE=https://github.com/rytkmt/slotlock/releases/download/$VERSION
```

### ソースからビルド

```sh
CGO_ENABLED=0 go build -o slotlock .
```

純 Go（`modernc.org/sqlite`）なので C コンパイラは不要です。`CGO_ENABLED=0` を付けます。

## データの保存先

割り当ては SQLite ファイルに保存されます。パスは次の順で決まります。

1. 環境変数 `$SLOTLOCK_DB`
2. `$XDG_DATA_HOME/slotlock/slotlock.db`
3. `~/.local/share/slotlock/slotlock.db`（上記が無い場合）

## herdr ラッパー（任意）

`slotlock-herdr` は key を自動的に `$HERDR_WORKSPACE_ID` で埋めるだけの薄い
ラッパーです。[herdr](https://herdr.dev) のワークスペースごとに安定した番号を
割り当てたいときに使います。slotlock 本体は herdr を知らず、このラッパーだけが
herdr に依存します。

```sh
# 第1引数にサブコマンドを渡す。key を要する run/get/release は --key が自動補完される。
slotlock-herdr run --scope rspec --min 2 --max 8 -- <command...>
slotlock-herdr get --scope rspec
slotlock-herdr list        # list/help は key を使わないのでそのまま素通し

# run の中身: slotlock run --key "$HERDR_WORKSPACE_ID" --scope rspec ... -- <command...>
```

## リリース（開発者向け）

`v*` タグを push すると GitHub Actions（`.github/workflows/release.yml`）が
バイナリをビルドし、`slotlock-herdr` と一緒に Release に添付します。

```sh
# <version> は次に出すバージョン（例: vX.Y.Z）。既存タグと重複しない値にする。
git tag <version> && git push origin <version>
```
