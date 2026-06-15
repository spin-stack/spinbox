// Command recorder produces an asciicast (v2) of the spinbox boot demo by
// driving `ctr run -t` under a PTY and capturing its output, headless.
//
// Unlike `asciinema rec -c "expect ..."`, it runs as a single process, does not
// render to a real terminal, and stays idle (read-only) during the VM boot, so
// the recording does not steal host CPU from the guest. That keeps the boot time
// shown by `systemd-analyze` close to what an interactive run reports. An
// optional warm-up run generates fsmeta/VMDK and warms caches before recording.
//
// Usage:
//
//	go run ./build/cast/recorder -out spinbox-demo.cast \
//	  -image ghcr.io/spin-stack/spinbox/sandbox:latest
package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"regexp"
	"sync"
	"syscall"
	"time"

	"github.com/creack/pty"
)

func main() {
	var (
		out         = flag.String("out", "spinbox-demo.cast", "output asciicast file")
		address     = flag.String("address", "/var/run/spin-stack/containerd.sock", "containerd address")
		image       = flag.String("image", "ghcr.io/spin-stack/spinbox/sandbox:latest", "sandbox image")
		snapshotter = flag.String("snapshotter", "spin-erofs", "snapshotter")
		runtime     = flag.String("runtime", "io.containerd.spinbox.v1", "runtime")
		name        = flag.String("name", "demo-vm", "container name")
		cols        = flag.Int("cols", 120, "terminal columns")
		rows        = flag.Int("rows", 32, "terminal rows")
		warmup      = flag.Bool("warmup", true, "do a discarded warm-up boot first (generate fsmeta/VMDK, warm caches)")
		warmupWait  = flag.Duration("warmup-wait", 5*time.Second, "how long to let the warm-up guest run before teardown")
		typeDelay   = flag.Duration("type-delay", 30*time.Millisecond, "per-character typing delay for typed commands")
		bootTimeout = flag.Duration("boot-timeout", 90*time.Second, "max wait for the guest login prompt")
		verbose     = flag.Bool("v", false, "verbose: mirror the live PTY output to stderr (for debugging)")
	)
	flag.Parse()

	if err := run(cfg{
		out:         *out,
		address:     *address,
		image:       *image,
		snapshotter: *snapshotter,
		runtime:     *runtime,
		name:        *name,
		cols:        *cols,
		rows:        *rows,
		warmup:      *warmup,
		warmupWait:  *warmupWait,
		typeDelay:   *typeDelay,
		bootTimeout: *bootTimeout,
		verbose:     *verbose,
	}); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

type cfg struct {
	out, address, image, snapshotter, runtime, name string
	cols, rows                                      int
	warmup, verbose                                 bool
	warmupWait, typeDelay, bootTimeout              time.Duration
}

func (c cfg) ctrArgs(extra ...string) []string {
	return append([]string{"--address", c.address}, extra...)
}

func run(c cfg) error {
	// Always start from a clean slate (handles a container left by a previous
	// interrupted run) and clean up on normal exit.
	teardown(c)
	defer teardown(c)

	// defer does not run on Ctrl-C, so also clean up on signals — mirrors the
	// `trap cleanup EXIT` in record.sh.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sigCh
		fmt.Fprintln(os.Stderr, "\ninterrupted, cleaning up...")
		teardown(c)
		os.Exit(130)
	}()

	if c.warmup {
		fmt.Fprintln(os.Stderr, "warm-up: pulling image...")
		pull := exec.Command("ctr", c.ctrArgs("image", "pull", "--snapshotter", c.snapshotter, c.image)...)
		pull.Stdout, pull.Stderr = os.Stderr, os.Stderr
		_ = pull.Run()

		// Boot the guest detached so its init (systemd) generates the parent's
		// fsmeta/VMDK and warms the EROFS/page caches, then tear it down. We do
		// NOT run a foreground command: the sandbox's init never exits, so a
		// foreground `ctr run` would block forever.
		fmt.Fprintf(os.Stderr, "warm-up: booting %s-warmup to generate fsmeta/VMDK (%s)...\n", c.name, c.warmupWait)
		warm := exec.Command("ctr", c.ctrArgs(
			"run", "-d", "--snapshotter", c.snapshotter, "--runtime", c.runtime,
			c.image, c.name+"-warmup")...)
		warm.Stdout, warm.Stderr = os.Stderr, os.Stderr
		if err := warm.Run(); err != nil {
			fmt.Fprintln(os.Stderr, "warm-up boot failed (continuing):", err)
		}
		time.Sleep(c.warmupWait)
		teardown(c) // removes the warm-up container/snapshot before recording
	}

	f, err := os.Create(c.out)
	if err != nil {
		return fmt.Errorf("create %s: %w", c.out, err)
	}
	defer f.Close()
	w := bufio.NewWriter(f)
	defer func() { _ = w.Flush() }()

	rec := &recorder{
		start:   time.Now(),
		w:       w,
		verbose: c.verbose,
		chunks:  make(chan timedChunk, 1024),
		done:    make(chan struct{}),
	}
	if c.verbose {
		rec.render = make(chan []byte, 1024)
		go rec.renderLoop()
	}
	if err := rec.writeHeader(c.cols, c.rows); err != nil {
		return err
	}

	// A plain bash session under the PTY; we drive it like an operator would.
	cmd := exec.Command("bash", "--norc", "--noprofile")
	cmd.Env = append(os.Environ(), "TERM=xterm-256color", fmt.Sprintf("COLUMNS=%d", c.cols), fmt.Sprintf("LINES=%d", c.rows))
	ptmx, err := pty.Start(cmd)
	if err != nil {
		return fmt.Errorf("start pty: %w", err)
	}
	defer func() { _ = ptmx.Close() }()
	_ = pty.Setsize(ptmx, &pty.Winsize{Rows: uint16(c.rows), Cols: uint16(c.cols)})
	rec.pty, rec.cols, rec.rows = ptmx, c.cols, c.rows

	go rec.read(ptmx)
	go rec.writeLoop()

	hostPrompt := regexp.MustCompile(`spinbox\$ `)
	guestPrompt := regexp.MustCompile(`root@[^\n]*# `)
	loginPrompt := regexp.MustCompile(`login: `)
	passwordPrompt := regexp.MustCompile(`[Pp]assword: `)

	d := &driver{rec: rec, typeDelay: c.typeDelay}

	// Host shell setup with a recognizable prompt.
	rec.logf("setting up host shell prompt")
	d.send(`PS1='\nspinbox$ '`)
	if err := rec.expect(hostPrompt, 5*time.Second); err != nil {
		return fmt.Errorf("host prompt not detected (got tail: %q): %w", rec.tail(200), err)
	}
	rec.logf("host shell ready")
	d.raw("clear\r")
	_ = rec.expect(hostPrompt, 5*time.Second)

	// Launch the VM. We only READ while it boots — no host-side work competes
	// with the guest, so systemd-analyze reflects the real boot time.
	rec.logf("launching VM (read-only during boot)...")
	d.send(fmt.Sprintf("ctr --address %s run -t --rm --snapshotter %s --runtime %s %s %s",
		c.address, c.snapshotter, c.runtime, c.image, c.name))

	// The guest either auto-logs-in (shell prompt directly) or shows a getty
	// "login:" prompt. Handle both.
	switch idx, err := rec.expectAny(c.bootTimeout, guestPrompt, loginPrompt); {
	case err != nil:
		return fmt.Errorf("no shell or login prompt after boot (tail: %q): %w", rec.tail(400), err)
	case idx == 1: // getty login
		rec.logf("guest login prompt; authenticating as root")
		d.send("root")
		// Some images ask for a password, some don't.
		pidx, perr := rec.expectAny(15*time.Second, guestPrompt, passwordPrompt)
		if perr != nil {
			return fmt.Errorf("after sending username (tail: %q): %w", rec.tail(200), perr)
		}
		if pidx == 1 { // password prompt
			d.send("spinbox")
			if err := rec.expect(guestPrompt, 15*time.Second); err != nil {
				return fmt.Errorf("login failed (tail: %q): %w", rec.tail(200), err)
			}
		}
	}
	rec.logf("guest shell reached")

	// Inside the guest: show the boot timing.
	for _, g := range []string{"systemd-analyze", "systemd-analyze critical-chain --no-pager", "systemd-analyze blame | head"} {
		rec.logf("guest: %s", g)
		d.send(g)
		if err := rec.expect(guestPrompt, 15*time.Second); err != nil {
			return fmt.Errorf("guest command %q stalled (tail: %q): %w", g, rec.tail(200), err)
		}
	}

	// Leave the guest (Ctrl-D), VM tears down, back to the host prompt.
	rec.logf("exiting guest (Ctrl-D), tearing down VM")
	d.raw("\x04")
	_ = rec.expect(hostPrompt, 30*time.Second)
	d.raw("exit\r")

	_ = cmd.Wait()
	_ = ptmx.Close() // unblock the reader so it closes the chunks channel
	<-rec.done       // wait for all buffered events to be written
	fmt.Fprintf(os.Stderr, "saved %s\n", c.out)
	return nil
}

func teardown(c cfg) {
	for _, n := range []string{c.name, c.name + "-warmup"} {
		_ = exec.Command("ctr", c.ctrArgs("task", "kill", n)...).Run()
		_ = exec.Command("ctr", c.ctrArgs("task", "delete", n)...).Run()
		_ = exec.Command("ctr", c.ctrArgs("container", "rm", n)...).Run()
		_ = exec.Command("ctr", c.ctrArgs("snapshots", "--snapshotter", c.snapshotter, "rm", n+"-snapshot")...).Run()
	}
}

// timedChunk is PTY output stamped with the time it was read, so cast event
// timing stays accurate even if the writer lags behind the reader.
type timedChunk struct {
	at   float64
	data []byte
}

// recorder writes asciicast v2 events and keeps a scrollback for prompt matching.
//
// The PTY reader is decoupled from event writing: read() does nothing but drain
// the PTY into the chunks channel as fast as possible, so slow processing (or
// the verbose mirror) can never back-pressure the guest's blocking serial
// console and inflate its boot time.
type recorder struct {
	mu      sync.Mutex
	start   time.Time
	w       *bufio.Writer
	scroll  []byte
	verbose bool

	pty        *os.File   // for typing and for answering terminal queries
	ptyMu      sync.Mutex // serializes writes to pty (driver + query responder)
	cols, rows int

	chunks chan timedChunk // reader -> writer
	render chan []byte     // writer -> stderr mirror (verbose; dropped under load)
	done   chan struct{}   // closed when all chunks have been written
}

// writePTY writes to the PTY, serialized so the typewriter driver and the query
// responder never interleave bytes.
func (r *recorder) writePTY(b []byte) {
	r.ptyMu.Lock()
	_, _ = r.pty.Write(b)
	r.ptyMu.Unlock()
}

// answerQueries replies to terminal status queries a guest boot component may
// send and block on. A real terminal answers instantly; without a reply the
// guest waits out a timeout, which shifts the whole userspace boot later.
//
//   - CPR  ESC[6n  -> ESC[<rows>;<cols>R   (cursor position; also used to probe size)
//   - DSR  ESC[5n  -> ESC[0n               (device status: OK)
//   - DA1  ESC[c / ESC[0c -> ESC[?1;2c     (device attributes: VT100 w/ AVO)
func (r *recorder) answerQueries(data []byte) {
	if bytes.Contains(data, []byte("\x1b[6n")) {
		r.writePTY(fmt.Appendf(nil, "\x1b[%d;%dR", r.rows, r.cols))
	}
	if bytes.Contains(data, []byte("\x1b[5n")) {
		r.writePTY([]byte("\x1b[0n"))
	}
	if bytes.Contains(data, []byte("\x1b[c")) || bytes.Contains(data, []byte("\x1b[0c")) {
		r.writePTY([]byte("\x1b[?1;2c"))
	}
}

// logf prints a timestamped progress line to stderr.
func (r *recorder) logf(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "[+%6.2fs] %s\n", time.Since(r.start).Seconds(), fmt.Sprintf(format, a...))
}

// tail returns the last n bytes of captured output (for diagnostics).
func (r *recorder) tail(n int) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.scroll) > n {
		return string(r.scroll[len(r.scroll)-n:])
	}
	return string(r.scroll)
}

func (r *recorder) writeHeader(cols, rows int) error {
	hdr := map[string]any{
		"version":   2,
		"width":     cols,
		"height":    rows,
		"timestamp": r.start.Unix(),
		"env":       map[string]string{"TERM": "xterm-256color", "SHELL": "/bin/bash"},
	}
	b, err := json.Marshal(hdr)
	if err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, err := r.w.Write(b); err != nil {
		return err
	}
	return r.w.WriteByte('\n')
}

// read drains the PTY as fast as possible into the chunks channel. It does no
// JSON/IO work itself, so it never blocks the guest console.
func (r *recorder) read(ptmx *os.File) {
	buf := make([]byte, 8192)
	for {
		n, err := ptmx.Read(buf)
		if n > 0 {
			cp := make([]byte, n)
			copy(cp, buf[:n])
			r.chunks <- timedChunk{at: time.Since(r.start).Seconds(), data: cp}
		}
		if err != nil {
			close(r.chunks)
			return
		}
	}
}

// writeLoop consumes chunks: writes cast events, updates the scrollback, and
// (verbose) forwards to the render mirror without ever blocking.
func (r *recorder) writeLoop() {
	for c := range r.chunks {
		ev, _ := json.Marshal([]any{c.at, "o", string(c.data)})

		r.mu.Lock()
		_, _ = r.w.Write(ev)
		_ = r.w.WriteByte('\n')
		_ = r.w.Flush() // flush per event so the cast survives a Ctrl-C mid-run
		const maxScroll = 1 << 16
		r.scroll = append(r.scroll, c.data...)
		if len(r.scroll) > maxScroll {
			r.scroll = r.scroll[len(r.scroll)-maxScroll:]
		}
		r.mu.Unlock()

		r.answerQueries(c.data)

		if r.render != nil {
			select {
			case r.render <- c.data:
			default: // drop the live mirror under load; never stall draining
			}
		}
	}
	if r.render != nil {
		close(r.render)
	}
	close(r.done)
}

// renderLoop mirrors output to stderr for -v, off the draining path.
func (r *recorder) renderLoop() {
	for b := range r.render {
		_, _ = os.Stderr.Write(b)
	}
}

// expect blocks until re matches new output or the timeout elapses, then
// consumes the scrollback up to the match so later expects don't re-match it.
func (r *recorder) expect(re *regexp.Regexp, timeout time.Duration) error {
	_, err := r.expectAny(timeout, re)
	return err
}

// expectAny waits until one of res matches new output, returning the index of
// the first (highest-priority) match, or an error on timeout. The scrollback is
// consumed up to the match.
func (r *recorder) expectAny(timeout time.Duration, res ...*regexp.Regexp) (int, error) {
	deadline := time.Now().Add(timeout)
	for {
		r.mu.Lock()
		for i, re := range res {
			if loc := re.FindIndex(r.scroll); loc != nil {
				r.scroll = r.scroll[loc[1]:]
				r.mu.Unlock()
				return i, nil
			}
		}
		r.mu.Unlock()
		if time.Now().After(deadline) {
			return -1, fmt.Errorf("timeout after %s waiting for %v", timeout, res)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// driver types into the PTY via the recorder's serialized writer.
type driver struct {
	rec       *recorder
	typeDelay time.Duration
}

// send types a command with a per-character delay (cosmetic typewriter effect at
// the prompt — never during boot) and presses Enter.
func (d *driver) send(line string) {
	for _, ch := range []byte(line) {
		d.rec.writePTY([]byte{ch})
		if d.typeDelay > 0 {
			time.Sleep(d.typeDelay)
		}
	}
	d.rec.writePTY([]byte("\r"))
}

// raw writes bytes verbatim (control sequences, no typing delay).
func (d *driver) raw(s string) { d.rec.writePTY([]byte(s)) }
