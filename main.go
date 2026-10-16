// slotlock — 汎用スロット番号ロッカー。
//
// 「同一 scope 内で key ごとに 1..max の整数スロットを安定的に割り当て、割り当て中は
// 同一 (scope, key) の同時実行を禁止する」ための小さな CLI。
//
// 代表的な用途は、並列テスト等で「プロセスごとに重複しない番号」を安定して得ること。
// 例: rspec の TEST_ENV_NUMBER 用に、呼び出し元（herdr ワークスペース等）ごとに
// 固定の番号を割り当て、同じ番号のテストDBを使い回す。
//
// 詳しい使い方は `slotlock help` を参照。
package main

import (
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"syscall"
	"text/tabwriter"
	"time"

	"slotlock/internal/lock"
	"slotlock/internal/store"
)

const defaultMaxSlots = 8
const defaultMinSlot = 1
const defaultTTL = 24 * time.Hour

func main() {
	if len(os.Args) < 2 {
		usage(os.Stderr)
		os.Exit(2)
	}
	switch os.Args[1] {
	case "run":
		os.Exit(cmdRun(os.Args[2:]))
	case "acquire":
		os.Exit(cmdAcquire(os.Args[2:]))
	case "list":
		os.Exit(cmdList(os.Args[2:]))
	case "get":
		os.Exit(cmdGet(os.Args[2:]))
	case "release":
		os.Exit(cmdRelease(os.Args[2:]))
	case "help", "-h", "--help":
		usage(os.Stdout)
		os.Exit(0)
	default:
		fmt.Fprintf(os.Stderr, "slotlock: unknown command %q\n\n", os.Args[1])
		usage(os.Stderr)
		os.Exit(2)
	}
}

// options は run/list/release 共通で使うフラグの寄せ集め。
type options struct {
	scope string
	key   string
	min   int
	max   int
	ttl   time.Duration
	dbt   string
	args  []string // "--" 以降（run のコマンド）
}

// parseFlags は簡易フラグパーサ。"--" で以降をコマンド引数として切り出す。
func parseFlags(argv []string) (options, error) {
	opt := options{min: defaultMinSlot, max: defaultMaxSlots, ttl: defaultTTL, dbt: defaultDBPath()}
	i := 0
	for i < len(argv) {
		a := argv[i]
		if a == "--" {
			opt.args = argv[i+1:]
			break
		}
		next := func() (string, error) {
			if i+1 >= len(argv) {
				return "", fmt.Errorf("flag %s requires a value", a)
			}
			i++
			return argv[i], nil
		}
		var err error
		switch a {
		case "--scope":
			opt.scope, err = next()
		case "--key":
			opt.key, err = next()
		case "--max":
			var v string
			if v, err = next(); err == nil {
				opt.max, err = strconv.Atoi(v)
			}
		case "--min":
			var v string
			if v, err = next(); err == nil {
				opt.min, err = strconv.Atoi(v)
			}
		case "--ttl":
			var v string
			if v, err = next(); err == nil {
				opt.ttl, err = time.ParseDuration(v)
			}
		case "--db":
			opt.dbt, err = next()
		default:
			return opt, fmt.Errorf("unknown flag %q", a)
		}
		if err != nil {
			return opt, err
		}
		i++
	}
	return opt, nil
}

func cmdRun(argv []string) int {
	opt, err := parseFlags(argv)
	if err != nil {
		fmt.Fprintln(os.Stderr, "slotlock run:", err)
		return 2
	}
	if opt.scope == "" || opt.key == "" {
		fmt.Fprintln(os.Stderr, "slotlock run: --scope and --key are required")
		return 2
	}
	if len(opt.args) == 0 {
		fmt.Fprintln(os.Stderr, "slotlock run: a command is required after --")
		return 2
	}
	if opt.max < 1 {
		fmt.Fprintln(os.Stderr, "slotlock run: --max must be >= 1")
		return 2
	}
	if opt.min < 1 {
		fmt.Fprintln(os.Stderr, "slotlock run: --min must be >= 1")
		return 2
	}
	if opt.min > opt.max {
		fmt.Fprintln(os.Stderr, "slotlock run: --min must be <= --max")
		return 2
	}

	st, err := store.Open(opt.dbt)
	if err != nil {
		fmt.Fprintln(os.Stderr, "slotlock:", err)
		return 1
	}
	defer st.Close()

	now := time.Now()
	slot, err := st.Acquire(opt.scope, opt.key, opt.min, opt.max, opt.ttl, now)
	if err == store.ErrNoSlotAvailable {
		fmt.Fprintf(os.Stderr,
			"slotlock: no slot available in scope %q (range %d..%d). all slots are assigned and in use within TTL.\n",
			opt.scope, opt.min, opt.max)
		return 1
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "slotlock:", err)
		return 1
	}

	// 同時実行の排他: この (scope, slot) のロックを非ブロッキングで掴む。
	lk, err := lock.TryAcquire(lockDir(opt.dbt), opt.scope+"-"+strconv.Itoa(slot))
	if err == lock.ErrBusy {
		fmt.Fprintf(os.Stderr,
			"slotlock: scope %q key %q (slot %d) is already running elsewhere. concurrent runs of the same (scope,key) are not allowed.\n",
			opt.scope, opt.key, slot)
		return 1
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "slotlock:", err)
		return 1
	}
	defer lk.Release()

	code := runCommand(slot, opt.args)

	// 実行完了時に last_used_at を更新（TTL の起点を「最終利用」にする）。
	_ = st.Touch(opt.scope, opt.key, time.Now())
	return code
}

// runCommand は SLOT_NUMBER を環境に入れてコマンドを実行し、その終了コードを返す。
func runCommand(slot int, args []string) int {
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = append(os.Environ(), "SLOT_NUMBER="+strconv.Itoa(slot))

	// シグナルを子プロセスへ中継する。
	sigc := make(chan os.Signal, 1)
	signal.Notify(sigc, syscall.SIGINT, syscall.SIGTERM)
	if err := cmd.Start(); err != nil {
		fmt.Fprintln(os.Stderr, "slotlock: failed to start command:", err)
		return 1
	}
	go func() {
		for s := range sigc {
			if cmd.Process != nil {
				_ = cmd.Process.Signal(s)
			}
		}
	}()
	err := cmd.Wait()
	signal.Stop(sigc)
	close(sigc)
	if err == nil {
		return 0
	}
	if ee, ok := err.(*exec.ExitError); ok {
		if ws, ok := ee.Sys().(syscall.WaitStatus); ok {
			if ws.Signaled() {
				return 128 + int(ws.Signal())
			}
			return ws.ExitStatus()
		}
		return ee.ExitCode()
	}
	fmt.Fprintln(os.Stderr, "slotlock:", err)
	return 1
}

func cmdList(argv []string) int {
	opt, err := parseFlags(argv)
	if err != nil {
		fmt.Fprintln(os.Stderr, "slotlock list:", err)
		return 2
	}
	st, err := store.Open(opt.dbt)
	if err != nil {
		fmt.Fprintln(os.Stderr, "slotlock:", err)
		return 1
	}
	defer st.Close()

	rows, err := st.List(opt.scope)
	if err != nil {
		fmt.Fprintln(os.Stderr, "slotlock:", err)
		return 1
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintln(w, "SCOPE\tSLOT\tKEY\tLAST_USED\tAGE")
	now := time.Now()
	for _, a := range rows {
		age := now.Sub(a.LastUsedAt).Truncate(time.Second)
		fmt.Fprintf(w, "%s\t%d\t%s\t%s\t%s\n",
			a.Scope, a.SlotNumber, a.Key, a.LastUsedAt.Format(time.RFC3339), age)
	}
	w.Flush()
	return 0
}

// cmdAcquire は (scope, key) に slot 番号を確保（永続割り当て）し、番号だけを stdout に
// 1 行出す。run と違い flock は掴まない（同時実行の排他は行わない）。未割り当てなら新規に
// 確保し、既にあればその番号を再利用する点が get（参照のみ）との違い。
func cmdAcquire(argv []string) int {
	opt, err := parseFlags(argv)
	if err != nil {
		fmt.Fprintln(os.Stderr, "slotlock acquire:", err)
		return 2
	}
	if opt.scope == "" || opt.key == "" {
		fmt.Fprintln(os.Stderr, "slotlock acquire: --scope and --key are required")
		return 2
	}
	if opt.max < 1 {
		fmt.Fprintln(os.Stderr, "slotlock acquire: --max must be >= 1")
		return 2
	}
	if opt.min < 1 {
		fmt.Fprintln(os.Stderr, "slotlock acquire: --min must be >= 1")
		return 2
	}
	if opt.min > opt.max {
		fmt.Fprintln(os.Stderr, "slotlock acquire: --min must be <= --max")
		return 2
	}

	st, err := store.Open(opt.dbt)
	if err != nil {
		fmt.Fprintln(os.Stderr, "slotlock:", err)
		return 1
	}
	defer st.Close()

	slot, err := st.Acquire(opt.scope, opt.key, opt.min, opt.max, opt.ttl, time.Now())
	if err == store.ErrNoSlotAvailable {
		fmt.Fprintf(os.Stderr,
			"slotlock: no slot available in scope %q (range %d..%d). all slots are assigned and in use within TTL.\n",
			opt.scope, opt.min, opt.max)
		return 1
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "slotlock:", err)
		return 1
	}
	// 番号だけを 1 行 stdout に出す（n=$(slotlock acquire ...) で受けられる）。
	fmt.Println(slot)
	return 0
}

func cmdGet(argv []string) int {
	opt, err := parseFlags(argv)
	if err != nil {
		fmt.Fprintln(os.Stderr, "slotlock get:", err)
		return 2
	}
	if opt.scope == "" || opt.key == "" {
		fmt.Fprintln(os.Stderr, "slotlock get: --scope and --key are required")
		return 2
	}
	st, err := store.Open(opt.dbt)
	if err != nil {
		fmt.Fprintln(os.Stderr, "slotlock:", err)
		return 1
	}
	defer st.Close()

	slot, ok, err := st.Get(opt.scope, opt.key)
	if err != nil {
		fmt.Fprintln(os.Stderr, "slotlock:", err)
		return 1
	}
	if !ok {
		// 未割り当ては stdout に何も出さず非ゼロ終了（スクリプトから判定しやすい）。
		fmt.Fprintf(os.Stderr, "slotlock: no slot assigned for scope=%q key=%q\n", opt.scope, opt.key)
		return 1
	}
	// 番号だけを 1 行 stdout に出す（n=$(slotlock get ...) で受けられる）。
	fmt.Println(slot)
	return 0
}

func cmdRelease(argv []string) int {
	opt, err := parseFlags(argv)
	if err != nil {
		fmt.Fprintln(os.Stderr, "slotlock release:", err)
		return 2
	}
	if opt.scope == "" || opt.key == "" {
		fmt.Fprintln(os.Stderr, "slotlock release: --scope and --key are required")
		return 2
	}
	st, err := store.Open(opt.dbt)
	if err != nil {
		fmt.Fprintln(os.Stderr, "slotlock:", err)
		return 1
	}
	defer st.Close()

	ok, err := st.Release(opt.scope, opt.key)
	if err != nil {
		fmt.Fprintln(os.Stderr, "slotlock:", err)
		return 1
	}
	if ok {
		fmt.Printf("released scope=%q key=%q\n", opt.scope, opt.key)
	} else {
		fmt.Printf("no assignment for scope=%q key=%q\n", opt.scope, opt.key)
	}
	return 0
}
