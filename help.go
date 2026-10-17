package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// defaultDBPath は SQLite ファイルのパスを決める。
// 優先度: $SLOTLOCK_DB > $XDG_DATA_HOME/slotlock/slotlock.db > ~/.local/share/slotlock/slotlock.db
func defaultDBPath() string {
	if p := os.Getenv("SLOTLOCK_DB"); p != "" {
		return p
	}
	base := os.Getenv("XDG_DATA_HOME")
	if base == "" {
		home, _ := os.UserHomeDir()
		base = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(base, "slotlock", "slotlock.db")
}

// lockDir は flock 用ロックファイルを置くディレクトリ。DB と同じ場所の locks/ 配下。
func lockDir(dbPath string) string {
	return filepath.Join(filepath.Dir(dbPath), "locks")
}

// usage は人間にも AI にも読める詳細ヘルプを出力する。
func usage(w io.Writer) {
	fmt.Fprint(w, `slotlock — assign a stable integer "slot" per key within a scope, and prevent
concurrent runs of the same (scope, key).

WHAT IT DOES (for AI agents: read this before using)
  slotlock hands out an integer slot number in the range min..max for each unique
  "key" inside a "scope". The mapping (scope, key) -> slot_number is PERSISTED in
  SQLite, so running the same (scope, key) again returns the SAME number. This lets
  a caller reuse a per-slot resource (e.g. a numbered test database) across runs.

  Two separate layers:
    1. Assignment  (persistent, in SQLite): which key owns which slot number.
    2. Concurrency (ephemeral, via flock):  the same (scope, key) cannot run twice
       at once. A second concurrent run fails fast with an error (it does NOT wait).

  Slots are reclaimed lazily by TTL: when a NEW key needs a slot and none are free,
  slotlock reuses the slot of an assignment whose last use is older than --ttl
  (default 24h). Idle assignments are never cleaned up in the background; they only
  get recycled when a new key actually needs one.

COMMANDS
  slotlock run     --scope S --key K [--min M] [--max N] [--ttl D] [--db PATH] -- CMD [ARGS...]
      Acquire a slot for (S, K), export SLOT_NUMBER=<n>, then exec CMD.
      The slot's concurrency lock is held for the lifetime of CMD and released
      automatically when CMD exits (even on crash). CMD's exit code is returned.

      Exit codes:
        0..N : the command's own exit code
        1    : no slot available (all taken within TTL), or (scope,key) already
               running, or an internal error
        2    : usage error (bad flags / missing args)

  slotlock acquire --scope S --key K [--min M] [--max N] [--ttl D] [--db PATH]
      Reserve (and persist) a slot for (S, K) and print it to stdout (one line).
      Unlike run, it does NOT take a concurrency lock (flock) and does NOT exec a
      command: it only assigns the number. Unlike get, it CREATES a new assignment
      when none exists. If (S, K) already has a slot, that number is returned and
      its last-used time is refreshed.
      Use this to decide a key's number ahead of time (e.g. to pre-create the
      numbered resource) without running anything yet.
      Exit 0 on success; exit 1 if no slot is available (all taken within TTL).

  slotlock list    [--scope S] [--db PATH]
      Show current assignments (scope, slot, key, last used, age).

  slotlock get     --scope S --key K [--db PATH]
      Print the slot number currently assigned to (S, K) to stdout (one line).
      Read-only: it does NOT assign a new slot, take a lock, or update last-used.
      Exit 0 if assigned; exit 1 (no stdout) if not assigned.

  slotlock release --scope S --key K [--db PATH]
      Forget the assignment for (S, K). Does not affect a running command.

  slotlock force-release --scope S --slot N [--db PATH]
      Forget the assignment occupying slot N in scope S, identified by NUMBER
      instead of key. Recovery tool for when you no longer know the key (look up
      the number with 'slotlock list'). Does not affect a running command and does
      not touch the flock (a running process keeps its lock until it exits).

  slotlock help
      Show this help.

FLAGS
  --scope S   Namespace for slot numbering. Different scopes number independently.
  --key   K   Identifier that owns a slot within the scope. Same key => same slot.
  --slot  N   Slot NUMBER to force-release (used only by force-release).
  --min   M   Lowest slot number. Default 1. Use this to reserve low numbers
              (e.g. --min 2 to keep slot 1 for an unnumbered/default resource).
  --max   N   Highest slot number (slots are M..N). Default 8.
  --ttl   D   Idle duration after which a slot may be recycled for a new key.
              Go duration syntax (e.g. 24h, 90m, 30m). Default 24h.
  --db    PATH  SQLite file path. Default: $SLOTLOCK_DB or
                $XDG_DATA_HOME/slotlock/slotlock.db (fallback ~/.local/share/...).

EXAMPLES
  # Give each caller a stable slot 1..8 and run a command with SLOT_NUMBER set.
  slotlock run --scope rspec --key "$HERDR_WORKSPACE_ID" --max 8 -- \
    bash -c 'echo using slot $SLOT_NUMBER'

  # Inspect assignments.
  slotlock list --scope rspec

  # Reserve a number up front without running anything (no concurrency lock).
  n=$(slotlock acquire --scope rspec --key "$HERDR_WORKSPACE_ID" --max 8)

  # Manually release one.
  slotlock release --scope rspec --key w7T

  # Force-release by number when the key is unknown (find it via list).
  slotlock force-release --scope rspec --slot 3

NOTES
  - slotlock itself knows nothing about herdr or rspec. Callers pass --scope/--key.
  - For a herdr-aware wrapper that fills --key from $HERDR_WORKSPACE_ID, use the
    accompanying wrapper script (e.g. slotlock-herdr).
`)
}
