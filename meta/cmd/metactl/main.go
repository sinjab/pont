// metactl — P1 read-only Messenger CLI for pont-meta (threads + full
// conversation reads + cookie write-back).
//
// License: AGPL-3.0-or-later — see ../LICENSE. This binary links
// go.mau.fi/mautrix-meta (AGPL-3.0, mautrix-meta developers); the
// combined work is distributed under AGPL-3.0.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"sort"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/rs/zerolog"
	"go.mau.fi/mautrix-meta/pkg/messagix"
	"go.mau.fi/mautrix-meta/pkg/instameow"
	"go.mau.fi/mautrix-meta/pkg/messagix/cookies"
	"go.mau.fi/mautrix-meta/pkg/messagix/methods"
	"go.mau.fi/mautrix-meta/pkg/messagix/socket"
	"go.mau.fi/mautrix-meta/pkg/messagix/table"
	"go.mau.fi/mautrix-meta/pkg/messagix/types"
)

const usage = `usage:
  metactl threads    <cookies.json> [collect-sec, default 20]
  metactl messages   <cookies.json> <thread_key> [collect-sec, default 12]
  metactl ig-threads <ig-cookies.json>
  metactl send       <cookies.json> <thread_key> <text>   # one shot, no retry`

func main() {
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(2)
	}
	verb, cookiePath := os.Args[1], os.Args[2]
	collect := 20 * time.Second
	threadKey := int64(0)

	switch verb {
	case "threads":
		if len(os.Args) > 3 {
			collect = parseDur(os.Args[3])
		}
	case "messages":
		if len(os.Args) < 4 {
			fmt.Fprintln(os.Stderr, usage)
			os.Exit(2)
		}
		threadKey = mustInt(os.Args[3])
		if len(os.Args) > 4 {
			collect = parseDur(os.Args[4])
		}
	case "ig-threads":
		igThreads(cookiePath)
		return
	case "send":
		if len(os.Args) < 5 {
			fmt.Fprintln(os.Stderr, usage)
			os.Exit(2)
		}
		threadKey = mustInt(os.Args[3])
		sendText(cookiePath, threadKey, os.Args[4])
		return
	default:
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(2)
	}

	log := zerolog.New(zerolog.ConsoleWriter{Out: os.Stderr, TimeFormat: "15:04:05"}).With().Timestamp().Logger()
	zerolog.SetGlobalLevel(zerolog.InfoLevel)

	ck := loadCookies(log, cookiePath)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	client := messagix.NewClient(ck, log, &messagix.Config{})

	var (
		mu       sync.Mutex
		threads  = map[int64]*table.LSDeleteThenInsertThread{}
		contacts = map[int64]string{}
		msgs     = map[string]*table.LSDeleteThenInsertMessage{}
		connected = make(chan struct{})
		connOnce sync.Once
	)

	client.SetEventHandler(func(_ context.Context, evt any) {
		switch e := evt.(type) {
		case *table.LSTable:
			mu.Lock()
			for _, t := range e.LSDeleteThenInsertThread {
				threads[t.ThreadKey] = t
			}
			for _, c := range e.LSDeleteThenInsertContact {
				contacts[c.Id] = c.Name
			}
			for _, m := range e.LSDeleteThenInsertMessage {
				msgs[m.MessageId] = &table.LSDeleteThenInsertMessage{
					Text: m.Text, ThreadKey: m.ThreadKey, TimestampMs: m.TimestampMs,
					MessageId: m.MessageId, SenderId: m.SenderId, SendStatus: m.SendStatus,
				}
			}
			for _, m := range e.LSInsertMessage {
				msgs[m.MessageId] = &table.LSDeleteThenInsertMessage{
					Text: m.Text, ThreadKey: m.ThreadKey, TimestampMs: m.TimestampMs,
					MessageId: m.MessageId, SenderId: m.SenderId, SendStatus: m.SendStatus,
				}
			}
			for _, m := range e.LSUpsertMessage {
				msgs[m.MessageId] = &table.LSDeleteThenInsertMessage{
					Text: m.Text, ThreadKey: m.ThreadKey, TimestampMs: m.TimestampMs,
					MessageId: m.MessageId, SenderId: m.SenderId, SendStatus: m.SendStatus,
				}
			}
			mu.Unlock()
			client.PostHandlePublishResponse(e)
		case *messagix.ConnectedEvent:
			connOnce.Do(func() { close(connected) })
			log.Info().Msg("socket connected")
		case *messagix.TransientDisconnectEvent:
			log.Warn().Err(e.Err).Msg("transient disconnect")
		case *messagix.PermanentErrorEvent:
			log.Error().Err(e.Err).Msg("permanent socket error")
		}
	})

	if _, _, err := client.LoadMessagesPage(ctx); err != nil {
		log.Fatal().Msgf("LoadMessagesPage: %v", err)
	}
	if err := client.Connect(ctx); err != nil {
		log.Fatal().Msgf("connect: %v", err)
	}

	if verb == "messages" {
		select {
		case <-connected:
		case <-time.After(15 * time.Second):
			log.Fatal().Msg("socket never connected")
		case <-ctx.Done():
			log.Fatal().Msg("interrupted")
		}
		// First fetch for a thread = CreateThreadTask (FetchMessagesTask
		// needs a reference message id for pagination — bridge backfill.go).
		var task socket.Task = &socket.CreateThreadTask{ThreadFBID: threadKey, SyncGroup: 1}
		if _, err := client.ExecuteTasks(ctx, task); err != nil {
			log.Fatal().Msgf("CreateThreadTask: %v", err)
		}
		log.Info().Int64("thread", threadKey).Msg("requested thread history")
	}

	select {
	case <-time.After(collect):
	case <-ctx.Done():
	}

	// Cookie write-back: the server rotates cookies during the run; keep the
	// freshest set on disk or the next login rides stale credentials
	// (consult r1 Q3).
	if raw, err := json.Marshal(client.GetCookies()); err == nil {
		_ = os.WriteFile(cookiePath, raw, 0o600)
	}

	mu.Lock()
	defer mu.Unlock()

	if verb == "threads" {
		list := make([]*table.LSDeleteThenInsertThread, 0, len(threads))
		e2ee, open := 0, 0
		for _, t := range threads {
			list = append(list, t)
			if t.ThreadType.IsWhatsApp() {
				e2ee++
			} else {
				open++
			}
		}
		sort.Slice(list, func(i, j int) bool { return list[i].LastActivityTimestampMs > list[j].LastActivityTimestampMs })
		fmt.Fprintf(os.Stderr, "\n=== %d threads — E2EE: %d · open: %d · contacts: %d ===\n", len(list), e2ee, open, len(contacts))
		fmt.Println("thread_key\te2ee\tlast_activity\tname\tsnippet")
		for _, t := range list {
			flag := "open"
			if t.ThreadType.IsWhatsApp() {
				flag = "E2EE"
			}
			name := t.ThreadName
			if name == "" {
				name = contacts[t.ThreadKey] // 1:1: thread key == other participant's fbid
			}
			if name == "" {
				name = "(unknown)"
			}
			fmt.Printf("%d\t%s\t%s\t%s\t%s\n", t.ThreadKey, flag,
				time.UnixMilli(t.LastActivityTimestampMs).Format("2006-01-02 15:04"), name, trim(t.Snippet, 50))
		}
		return
	}

	// messages verb
	var list []*table.LSDeleteThenInsertMessage
	for _, m := range msgs {
		if m.ThreadKey == threadKey {
			list = append(list, m)
		}
	}
	sort.Slice(list, func(i, j int) bool { return list[i].TimestampMs < list[j].TimestampMs })
	fmt.Fprintf(os.Stderr, "\n=== %d messages in thread %d ===\n", len(list), threadKey)
	self := client.GetCookies().GetUserID()
	for _, m := range list {
		sender := contacts[m.SenderId]
		if sender == "" {
			if m.SenderId == self {
				sender = "YOU"
			} else {
				sender = strconv.FormatInt(m.SenderId, 10)
			}
		} else if m.SenderId == self {
			sender = "YOU (" + sender + ")"
		}
		stamp := time.UnixMilli(m.TimestampMs).Format("2006-01-02 15:04")
		fmt.Printf("%s\t%s\t%s\n", stamp, sender, m.Text)
	}
}

// sendText — one-shot text send with optimistic-ID verification.
// Exit codes: 0 = confirmed sent (message id on stdout), 1 = rejected
// by server, 3 = MAYBE sent (write attempted, confirmation lost — do
// NOT blind-retry with a fresh OTID; re-read the thread first).
func sendText(cookiePath string, threadKey int64, text string) {
	log := zerolog.New(zerolog.ConsoleWriter{Out: os.Stderr, TimeFormat: "15:04:05"}).With().Timestamp().Logger()
	zerolog.SetGlobalLevel(zerolog.InfoLevel)
	ck := loadCookies(log, cookiePath)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	client := messagix.NewClient(ck, log, &messagix.Config{})
	connected := make(chan struct{})
	var connOnce sync.Once
	client.SetEventHandler(func(_ context.Context, evt any) {
		if _, ok := evt.(*table.LSTable); ok {
			// keep sync cursors advancing while we hold the socket
			if tbl, ok2 := evt.(*table.LSTable); ok2 {
				client.PostHandlePublishResponse(tbl)
			}
		}
		if _, ok := evt.(*messagix.ConnectedEvent); ok {
			connOnce.Do(func() { close(connected) })
		}
	})

	if _, _, err := client.LoadMessagesPage(ctx); err != nil {
		log.Fatal().Msgf("LoadMessagesPage: %v", err)
	}
	if err := client.Connect(ctx); err != nil {
		log.Fatal().Msgf("connect: %v", err)
	}
	select {
	case <-connected:
	case <-time.After(15 * time.Second):
		log.Fatal().Msg("socket never connected")
	case <-ctx.Done():
		log.Fatal().Msg("interrupted")
	}

	if err := client.WaitUntilCanSendMessages(ctx, 15*time.Second); err != nil {
		log.Fatal().Msgf("cannot send yet: %v", err)
	}

	otid := methods.GenerateEpochID()
	task := &socket.SendMessageTask{
		ThreadId:         threadKey,
		Otid:             otid,
		Source:           table.MESSENGER_INBOX_IN_THREAD,
		InitiatingSource: table.FACEBOOK_INBOX,
		SendType:         table.TEXT,
		SyncGroup:        1,
		Text:             text,
	}
	resp, err := client.ExecuteTasks(ctx, task)
	if raw, jerr := json.Marshal(client.GetCookies()); jerr == nil {
		_ = os.WriteFile(cookiePath, raw, 0o600)
	}
	if err != nil {
		// The write MAY have landed (bridge maybeSent semantics) — re-read
		// the thread before deciding; never blind-retry.
		fmt.Fprintf(os.Stderr, "MAYBE-SENT: ExecuteTasks failed after write: %v — re-read the thread before retrying\n", err)
		os.Exit(3)
	}
	otidStr := strconv.FormatInt(otid, 10)
	if resp != nil {
		for _, r := range resp.LSReplaceOptimsiticMessage {
			if r.OfflineThreadingId == otidStr {
				fmt.Printf("sent\t%s\t%s\n", r.MessageId, time.Now().Format(time.RFC3339))
				return
			}
		}
		for _, f := range resp.LSMarkOptimisticMessageFailed {
			fmt.Fprintf(os.Stderr, "REJECTED: %s\n", f.Message)
			os.Exit(1)
		}
		for _, f := range resp.LSHandleFailedTask {
			fmt.Fprintf(os.Stderr, "REJECTED: %s\n", f.Message)
			os.Exit(1)
		}
	}
	fmt.Fprintln(os.Stderr, "MAYBE-SENT: no confirmation in response — re-read the thread before retrying")
	os.Exit(3)
}

func igThreads(cookiePath string) {
	log := zerolog.New(zerolog.ConsoleWriter{Out: os.Stderr, TimeFormat: "15:04:05"}).With().Timestamp().Logger()
	zerolog.SetGlobalLevel(zerolog.InfoLevel)

	ck := loadCookies(log, cookiePath)
	ck.Platform = types.Instagram

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// DisableTyping: skip the mqttbypass/streamcontroller side sockets
	// (consult r1 Q9) — one-shot inbox read only.
	client := instameow.NewClient(instameow.ClientParams{
		Cookies:       ck,
		Log:           log,
		DisableTyping: true,
	})

	viewer, mailbox, err := client.LoadIndex(ctx)
	if err != nil {
		log.Fatal().Msgf("LoadIndex: %v", err)
	}
	if viewer != nil {
		log.Info().Str("user_id", viewer.GetUserId()).Msg("logged in")
	}

	if raw, err := json.Marshal(client.GetCookies()); err == nil {
		_ = os.WriteFile(cookiePath, raw, 0o600)
	}

	type row struct {
		ts    int64
		line  string
	}
	var rows []row
	seen := map[string]bool{}
	for _, edge := range mailbox.ThreadsByFolder.Edges {
		ti := edge.Node.AsIGDirectThread
		if ti == nil || seen[ti.ThreadID] {
			continue
		}
		seen[ti.ThreadID] = true
		name := ti.ThreadTitle
		if name == "" && len(ti.Users) > 0 {
			for _, u := range ti.Users {
				n := u.FullName
				if n == "" {
					n = u.Username
				}
				if name == "" {
					name = n
				} else {
					name += ", " + n
				}
			}
		}
		kind := "1:1"
		if ti.IsGroup {
			kind = "group"
		}
		snippet := ""
		if ti.SlideMessages != nil && len(ti.SlideMessages.Edges) > 0 {
			last := ti.SlideMessages.Edges[len(ti.SlideMessages.Edges)-1].Node
			if last != nil {
				snippet = trim(last.TextBody, 50)
				if snippet == "" {
					snippet = trim(last.IGDSnippet, 50)
				}
			}
		}
		ts := ti.LastActivityTimestampMS.UnixMilli()
		rows = append(rows, row{ts, fmt.Sprintf("%s\t%s\t%s\t%d\t%s\t%s",
			ti.ThreadID, ti.Folder, kind, ti.ThreadKey,
			time.UnixMilli(ts).Format("2006-01-02 15:04"), name+"\t"+snippet)})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].ts > rows[j].ts })
	fmt.Fprintf(os.Stderr, "\n=== %d IG threads ===\n", len(rows))
	fmt.Println("thread_id\tfolder\tkind\tthread_key\tlast_activity\tname\tsnippet")
	for _, r := range rows {
		fmt.Println(r.line)
	}
}

func loadCookies(log zerolog.Logger, path string) *cookies.Cookies {
	raw, err := os.ReadFile(path)
	if err != nil {
		log.Fatal().Msgf("read cookies: %v", err)
	}
	ck := &cookies.Cookies{}
	if err := json.Unmarshal(raw, ck); err != nil {
		log.Fatal().Msgf("parse cookies: %v", err)
	}
	ck.Platform = types.Facebook
	return ck
}

func parseDur(s string) time.Duration {
	n, err := strconv.Atoi(s)
	if err != nil || n <= 0 || n > 300 {
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(2)
	}
	return time.Duration(n) * time.Second
}

func mustInt(s string) int64 {
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		fmt.Fprintln(os.Stderr, "bad thread_key: "+s)
		os.Exit(2)
	}
	return n
}

func trim(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
