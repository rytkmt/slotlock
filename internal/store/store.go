// Package store は slotlock の割り当てマッピングを SQLite に永続化する。
//
// データモデル:
//
//	slot_assignments(scope, key, slot_number, last_used_at)
//	  PRIMARY KEY (scope, key)
//	  UNIQUE      (scope, slot_number)
//
// 「割り当て」は永続（コマンド終了後も残す）。同じ (scope, key) で再実行すると
// 同じ slot_number を返すことで、呼び出し側はスロットに紐づく資源（テストDB等）を
// 使い回せる。使われなくなった割り当ては TTL（最終利用からの経過）で遅延回収する。
package store

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

// ErrNoSlotAvailable は、未使用スロットも TTL 切れスロットも無く割り当てできない場合に返す。
var ErrNoSlotAvailable = errors.New("no slot available")

// Assignment は 1 件の割り当てレコード。
type Assignment struct {
	Scope      string
	Key        string
	SlotNumber int
	LastUsedAt time.Time
}

// Store は SQLite 永続ストア。
type Store struct {
	db *sql.DB
}

// Open は DB ファイルを開き（無ければ作成し）スキーマを用意する。
func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("create db dir: %w", err)
	}
	// busy_timeout を付け、並行プロセスの短時間ロック競合を待てるようにする。
	dsn := "file:" + path + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}
	if _, err := db.Exec(`
CREATE TABLE IF NOT EXISTS slot_assignments (
  scope        TEXT    NOT NULL,
  key          TEXT    NOT NULL,
  slot_number  INTEGER NOT NULL,
  last_used_at INTEGER NOT NULL,
  PRIMARY KEY (scope, key)
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_scope_slot
  ON slot_assignments(scope, slot_number);
`); err != nil {
		db.Close()
		return nil, fmt.Errorf("init schema: %w", err)
	}
	return &Store{db: db}, nil
}

// Close は DB を閉じる。
func (s *Store) Close() error { return s.db.Close() }

// Acquire は (scope, key) に slot 番号を割り当て、last_used_at を now に更新して返す。
//
//   - 既に割り当てがあればその番号を再利用する（last_used_at を更新）。
//   - 無ければ min..max の中から「未使用」または「TTL 切れ（now-last_used_at > ttl）の
//     他 key が持つ番号」を選び、割り当てる。
//   - いずれも無ければ ErrNoSlotAvailable を返す。
//
// 割り当て処理は単一トランザクション内で行い、SQLite のロックで直列化する。
func (s *Store) Acquire(scope, key string, min, max int, ttl time.Duration, now time.Time) (int, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	nowUnix := now.Unix()

	// 1. 既存割り当てがあれば再利用。
	var slot int
	err = tx.QueryRow(
		`SELECT slot_number FROM slot_assignments WHERE scope = ? AND key = ?`,
		scope, key,
	).Scan(&slot)
	switch {
	case err == nil:
		if _, err := tx.Exec(
			`UPDATE slot_assignments SET last_used_at = ? WHERE scope = ? AND key = ?`,
			nowUnix, scope, key,
		); err != nil {
			return 0, err
		}
		return slot, tx.Commit()
	case errors.Is(err, sql.ErrNoRows):
		// 続行して新規割り当て。
	default:
		return 0, err
	}

	// 2. この scope で使用中の番号を集める。
	used := map[int]int64{} // slot_number -> last_used_at
	rows, err := tx.Query(
		`SELECT slot_number, last_used_at FROM slot_assignments WHERE scope = ?`,
		scope,
	)
	if err != nil {
		return 0, err
	}
	for rows.Next() {
		var n int
		var lu int64
		if err := rows.Scan(&n, &lu); err != nil {
			rows.Close()
			return 0, err
		}
		used[n] = lu
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}

	ttlSec := int64(ttl.Seconds())

	// 3. 未使用の最小番号を探す。
	for n := min; n <= max; n++ {
		if _, inUse := used[n]; !inUse {
			if err := s.insert(tx, scope, key, n, nowUnix); err != nil {
				return 0, err
			}
			return n, tx.Commit()
		}
	}

	// 4. 未使用が無ければ TTL 切れの番号を回収する（最も古いものから）。
	var staleSlot = -1
	var staleOldest int64
	for n, lu := range used {
		if n < min || n > max {
			continue // 範囲外（別の min/max 設定で割り当てられた番号）は回収対象にしない。
		}
		if nowUnix-lu > ttlSec {
			if staleSlot == -1 || lu < staleOldest {
				staleSlot = n
				staleOldest = lu
			}
		}
	}
	if staleSlot != -1 {
		// 旧 key のレコードを削除してから新 key を挿入する。
		if _, err := tx.Exec(
			`DELETE FROM slot_assignments WHERE scope = ? AND slot_number = ?`,
			scope, staleSlot,
		); err != nil {
			return 0, err
		}
		if err := s.insert(tx, scope, key, staleSlot, nowUnix); err != nil {
			return 0, err
		}
		return staleSlot, tx.Commit()
	}

	return 0, ErrNoSlotAvailable
}

func (s *Store) insert(tx *sql.Tx, scope, key string, slot int, nowUnix int64) error {
	_, err := tx.Exec(
		`INSERT INTO slot_assignments (scope, key, slot_number, last_used_at) VALUES (?, ?, ?, ?)`,
		scope, key, slot, nowUnix,
	)
	return err
}

// Touch は (scope, key) の last_used_at を更新する（コマンド実行完了時に呼ぶ想定）。
func (s *Store) Touch(scope, key string, now time.Time) error {
	_, err := s.db.Exec(
		`UPDATE slot_assignments SET last_used_at = ? WHERE scope = ? AND key = ?`,
		now.Unix(), scope, key,
	)
	return err
}

// Release は (scope, key) の割り当てを削除する。
func (s *Store) Release(scope, key string) (bool, error) {
	res, err := s.db.Exec(
		`DELETE FROM slot_assignments WHERE scope = ? AND key = ?`,
		scope, key,
	)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// List は割り当て一覧を返す。scope が空なら全件。
func (s *Store) List(scope string) ([]Assignment, error) {
	var (
		rows *sql.Rows
		err  error
	)
	if scope == "" {
		rows, err = s.db.Query(
			`SELECT scope, key, slot_number, last_used_at FROM slot_assignments
			 ORDER BY scope, slot_number`)
	} else {
		rows, err = s.db.Query(
			`SELECT scope, key, slot_number, last_used_at FROM slot_assignments
			 WHERE scope = ? ORDER BY slot_number`, scope)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Assignment
	for rows.Next() {
		var a Assignment
		var lu int64
		if err := rows.Scan(&a.Scope, &a.Key, &a.SlotNumber, &lu); err != nil {
			return nil, err
		}
		a.LastUsedAt = time.Unix(lu, 0)
		out = append(out, a)
	}
	return out, rows.Err()
}
