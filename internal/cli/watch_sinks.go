package cli

// Watch fan-out sinks (P2 tail, rewrite-consult Oct 11 2026): opt-in
// extras layered on top of the two mandatory outputs — the durable
// journal (write-first) and the stdout JSONL stream. Neither sink is
// load-bearing for durability: a subscriber death or hook failure can
// never kill the watcher, and every event is already journaled before
// any sink sees it (restart + `watch --since-seq N` replays the gap).
//
//	--socket <path>   Unix-domain broadcast bus: every live event line
//	                  (same JSONL bytes as stdout) is written to every
//	                  connected subscriber; subscribers may connect and
//	                  disconnect at any time. Live events only — the
//	                  journal replay path stays stdout-only.
//	--on-message <c>  Hook invoked per MESSAGE event with the JSON line
//	                  on stdin (run via sh -c, async, capped, 60s cap).
import (
	"bytes"
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"sync"
	"time"
)

// Sink flags (wired in watch.go init; reset in resetFlagGlobals).
var (
	watchSocket    string
	watchOnMessage string
)

// hookMaxConcurrent bounds in-flight hook invocations: a flooding chat
// must never fork-bomb the watcher. When saturated, the event's hook
// invocation is SKIPPED with a warning — the journal still holds it.
const hookMaxConcurrent = 8

// hookTimeout caps a single hook invocation.
const hookTimeout = time.Minute

// socketSink broadcasts emitted event lines to Unix-socket subscribers.
// A slow or dead subscriber never blocks the watcher for more than
// socketWriteTimeout and is dropped on the first failure.
type socketSink struct {
	mu     sync.Mutex
	conns  []net.Conn
	ln     net.Listener
	path   string
	closed bool
}

var watchSock *socketSink

const socketWriteTimeout = time.Second

// startSocketSink binds the Unix socket, removing any stale socket file
// left by a previous run (the terminal exit paths cannot run defers).
func startSocketSink(path string) error {
	if path == "" {
		return nil
	}
	if _, err := os.Stat(path); err == nil {
		if err := os.Remove(path); err != nil {
			return fmt.Errorf("watch: stale socket %s not removable: %w", path, err)
		}
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		return fmt.Errorf("watch: socket listen %s failed: %w", path, err)
	}
	s := &socketSink{ln: ln, path: path}
	watchSock = s
	go s.acceptLoop()
	fmt.Fprintf(os.Stderr, "watch: socket sink live on %s\n", path)
	return nil
}

// closeSocketSink stops the listener and removes the socket file.
func closeSocketSink() {
	s := watchSock
	if s == nil {
		return
	}
	watchSock = nil
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	_ = s.ln.Close()
	for _, c := range s.conns {
		_ = c.Close()
	}
	s.conns = nil
	_ = os.Remove(s.path)
}

func (s *socketSink) acceptLoop() {
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			// Listener closed (shutdown) — loop ends.
			return
		}
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			_ = conn.Close()
			return
		}
		s.conns = append(s.conns, conn)
		n := len(s.conns)
		s.mu.Unlock()
		fmt.Fprintf(os.Stderr, "watch: socket subscriber connected (%d)\n", n)
	}
}

// subscriberCount reports connected subscribers (used by tests as a
// registration barrier: a dialed connection only counts once the
// accept loop has registered it — connect-success alone is not
// subscription, a window consumers must tolerate via --since-seq).
func (s *socketSink) subscriberCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.conns)
}

// broadcast writes one event line to every subscriber, preserving order
// (single mutex), and drops any subscriber that fails or stalls.
func (s *socketSink) broadcast(line []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || len(s.conns) == 0 {
		return
	}
	kept := s.conns[:0]
	for _, c := range s.conns {
		_ = c.SetWriteDeadline(time.Now().Add(socketWriteTimeout))
		if _, err := c.Write(line); err != nil {
			_ = c.Close()
			fmt.Fprintf(os.Stderr, "watch: socket subscriber dropped: %v\n", err)
			continue
		}
		kept = append(kept, c)
	}
	// nil out the tail so dropped conns are collectable.
	for i := len(kept); i < len(s.conns); i++ {
		s.conns[i] = nil
	}
	s.conns = kept
}

// hookSem caps concurrent hook processes (never blocks the emit path:
// a saturated semaphore skips the invocation with a warning).
var hookSem = make(chan struct{}, hookMaxConcurrent)

// fireMessageHook invokes the --on-message command asynchronously with
// the event line on stdin. Failures are logged, never fatal.
func fireMessageHook(cmdline, payload []byte) {
	select {
	case hookSem <- struct{}{}:
	default:
		fmt.Fprintln(os.Stderr, "watch: hook skipped (saturated)")
		return
	}
	go func() {
		defer func() { <-hookSem }()
		ctx, cancel := context.WithTimeout(context.Background(), hookTimeout)
		defer cancel()
		cmd := exec.CommandContext(ctx, "sh", "-c", string(cmdline))
		cmd.Stdin = bytes.NewReader(payload)
		cmd.Stdout = os.Stderr
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			fmt.Fprintf(os.Stderr, "watch: hook failed: %v\n", err)
		}
	}()
}
