// seed — P0 read-only Messenger inbox probe for pont-meta.
//
// Prints every thread the initial sync exposes, flagging E2EE-cutover
// threads (ThreadType ENCRYPTED_OVER_WA_*, whose message bodies do NOT
// arrive over Lightspeed and need the E2EE companion lane).
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
	"sync"
	"syscall"
	"time"

	"github.com/rs/zerolog"
	"go.mau.fi/mautrix-meta/pkg/messagix"
	"go.mau.fi/mautrix-meta/pkg/messagix/cookies"
	"go.mau.fi/mautrix-meta/pkg/messagix/table"
	"go.mau.fi/mautrix-meta/pkg/messagix/types"
)

const usage = "usage: seed <cookies.json> [collect-seconds, default 25]"

func fatal(log zerolog.Logger, format string, args ...any) {
	log.Fatal().Msgf(format, args...)
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(2)
	}
	collect := 25 * time.Second
	if len(os.Args) > 2 {
		if d, err := time.ParseDuration(os.Args[2] + "s"); err == nil {
			collect = d
		} else {
			fmt.Fprintln(os.Stderr, usage)
			os.Exit(2)
		}
	}

	log := zerolog.New(zerolog.NewConsoleWriter()).With().Timestamp().Logger()

	raw, err := os.ReadFile(os.Args[1])
	if err != nil {
		fatal(log, "read cookies: %v", err)
	}
	ck := &cookies.Cookies{}
	if err := json.Unmarshal(raw, ck); err != nil {
		fatal(log, "parse cookies (want flat {name:value} JSON): %v", err)
	}
	ck.Platform = types.Facebook
	if missing := ck.GetMissingCookieNames(); len(missing) > 0 {
		log.Warn().Any("missing", missing).Msg("some FB cookies absent — login may fail")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	client := messagix.NewClient(ck, log, &messagix.Config{})

	var (
		mu      sync.Mutex
		threads = map[int64]*table.LSDeleteThenInsertThread{}
		tables  int
		e2eeCtr, openCtr int
	)

	client.SetEventHandler(func(_ context.Context, evt any) {
		switch e := evt.(type) {
		case *table.LSTable:
			mu.Lock()
			for _, t := range e.LSDeleteThenInsertThread {
				threads[t.ThreadKey] = t
			}
			tables++
			mu.Unlock()
			// Sync-cursor law (consult r1, agy risk 5): advance cursors or
			// pagination + DumpState break.
			client.PostHandlePublishResponse(e)
		case *messagix.ConnectedEvent:
			log.Info().Msg("socket connected — collecting initial tables")
		case *messagix.ReconnectedEvent:
			log.Info().Msg("socket reconnected")
		case *messagix.TransientDisconnectEvent:
			log.Warn().Err(e.Err).Int("attempts", e.ConnectionAttempts).Msg("transient disconnect")
		case *messagix.PermanentErrorEvent:
			log.Error().Err(e.Err).Msg("permanent socket error")
		}
	})

	ui, tbl, err := client.LoadMessagesPage(ctx)
	if err != nil {
		fatal(log, "LoadMessagesPage: %v", err)
	}
	if ui != nil {
		log.Info().Str("username", ui.GetUsername()).Str("name", ui.GetName()).Int64("fbid", ui.GetFBID()).Msg("account")
	}
	if tbl != nil {
		mu.Lock()
		for _, t := range tbl.LSDeleteThenInsertThread {
			threads[t.ThreadKey] = t
		}
		mu.Unlock()
	}

	if err := client.Connect(ctx); err != nil {
		fatal(log, "connect: %v", err)
	}

	select {
	case <-time.After(collect):
	case <-ctx.Done():
	}

	mu.Lock()
	list := make([]*table.LSDeleteThenInsertThread, 0, len(threads))
	for _, t := range threads {
		list = append(list, t)
		if t.ThreadType.IsWhatsApp() {
			e2eeCtr++
		} else {
			openCtr++
		}
	}
	mu.Unlock()
	sort.Slice(list, func(i, j int) bool {
		return list[i].LastActivityTimestampMs > list[j].LastActivityTimestampMs
	})

	fmt.Fprintf(os.Stderr, "\n=== %d threads (%d tables) — E2EE-cutover: %d · open: %d ===\n", len(list), tables, e2eeCtr, openCtr)
	fmt.Println("thread_key\te2ee\ttype\tfolder\tlast_activity\tname\tsnippet")
	for _, t := range list {
		flag := "open"
		if t.ThreadType.IsWhatsApp() {
			flag = "E2EE"
		}
		name := t.ThreadName
		if name == "" {
			name = "(unnamed 1:1)"
		}
		fmt.Printf("%d\t%s\t%d\t%s\t%s\t%s\t%s\n",
			t.ThreadKey, flag, int(t.ThreadType), t.FolderName,
			time.UnixMilli(t.LastActivityTimestampMs).Format("2006-01-02 15:04"), name, trim(t.Snippet, 60))
	}

}

func trim(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
