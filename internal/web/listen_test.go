package web

import (
	"bufio"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestParseListen(t *testing.T) {
	cases := []struct {
		in, kind, ch string
		bad          bool
	}{
		{"LISTEN jobs", "LISTEN", "jobs", false},
		{"listen Jobs;", "LISTEN", "jobs", false},
		{`LISTEN "My Chan"`, "LISTEN", "My Chan", false},
		{`LISTEN "a""b"`, "LISTEN", `a"b`, false},
		{"UNLISTEN *", "UNLISTEN", "*", false},
		{"UNLISTEN jobs", "UNLISTEN", "jobs", false},
		{"LISTEN *", "", "", true},
		{"LISTEN", "", "", true},
		{"LISTEN a b", "", "", true},
		{`LISTEN "a"b"`, "", "", true},
		{"LISTEN a;drop table t", "", "", true},
	}
	for _, c := range cases {
		kind, ch, err := parseListen(c.in)
		if c.bad {
			if err == nil {
				t.Errorf("%q: expected an error, got %q %q", c.in, kind, ch)
			}
			continue
		}
		if err != nil || kind != c.kind || ch != c.ch {
			t.Errorf("%q: got %q %q %v, want %q %q", c.in, kind, ch, err, c.kind, c.ch)
		}
	}
}

func TestListenerReceivesNotifyAndEndsOnUnlisten(t *testing.T) {
	pool := testPool(t, 4)
	ctx := context.Background()
	t.Cleanup(func() { closeListeners(testServerID, "") })

	intercept := listenIntercept(ctx, "t-listen", pool, testServerID, "db")
	if tag, ok, err := intercept("LISTEN t_chan"); !ok || err != nil || tag != "LISTEN" {
		t.Fatalf("LISTEN: tag=%q ok=%v err=%v", tag, ok, err)
	}
	l := listenerFor("t-listen")
	if l == nil {
		t.Fatal("no listener after LISTEN")
	}
	events, unsub := l.subscribe()
	defer unsub()

	if _, err := pool.Exec(ctx, "SELECT pg_notify('t_chan', 'hello')"); err != nil {
		t.Fatal(err)
	}
	select {
	case ev := <-events:
		if ev.Channel != "t_chan" || ev.Payload != "hello" {
			t.Fatalf("event = %+v", ev)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no notification within 3s")
	}

	// A second channel is added while the wait loop is running.
	if _, ok, err := intercept("LISTEN t_chan2"); !ok || err != nil {
		t.Fatalf("second LISTEN: ok=%v err=%v", ok, err)
	}
	if _, _, err := intercept("UNLISTEN t_chan"); err != nil {
		t.Fatal(err)
	}
	if listenerFor("t-listen") == nil {
		t.Fatal("listener ended while a channel was still listened to")
	}
	if _, _, err := intercept("UNLISTEN *"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-l.done:
	case <-time.After(3 * time.Second):
		t.Fatal("listener did not end after the last UNLISTEN")
	}
	if listenerFor("t-listen") != nil {
		t.Fatal("listener still registered")
	}

	if _, ok, _ := listenIntercept(ctx, "t-listen", pool, testServerID, "db")("SELECT 1"); ok {
		t.Fatal("non-LISTEN statement was intercepted")
	}
}

func TestListenStreamDeliversEventsAndClosed(t *testing.T) {
	pool := testPool(t, 4)
	ctx := context.Background()
	t.Cleanup(func() { closeListeners(testServerID, "") })

	intercept := listenIntercept(ctx, "t-stream", pool, testServerID, "db")
	if _, _, err := intercept("LISTEN t_stream"); err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(http.HandlerFunc((&Server{}).handleListenStream))
	defer srv.Close()
	resp, err := http.Get(srv.URL + "?tab_id=t-stream")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	lines := make(chan string, 16)
	go func() {
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			lines <- sc.Text()
		}
		close(lines)
	}()
	waitFor := func(substr string) {
		t.Helper()
		deadline := time.After(4 * time.Second)
		for {
			select {
			case l, ok := <-lines:
				if !ok {
					t.Fatalf("stream ended before %q", substr)
				}
				if strings.Contains(l, substr) {
					return
				}
			case <-deadline:
				t.Fatalf("timed out waiting for %q", substr)
			}
		}
	}

	waitFor(": connected")
	if _, err := pool.Exec(ctx, "SELECT pg_notify('t_stream', 'p1')"); err != nil {
		t.Fatal(err)
	}
	waitFor(`"payload":"p1"`)
	if _, _, err := intercept("UNLISTEN *"); err != nil {
		t.Fatal(err)
	}
	waitFor("event: closed")

	// No listener any more: a new stream is refused.
	r2, err := http.Get(srv.URL + "?tab_id=t-stream")
	if err != nil {
		t.Fatal(err)
	}
	r2.Body.Close()
	if r2.StatusCode != http.StatusNotFound {
		t.Fatalf("stream without listener: status %d, want 404", r2.StatusCode)
	}
}

func TestCloseListenersByServer(t *testing.T) {
	pool := testPool(t, 4)
	ctx := context.Background()
	if _, _, err := listenIntercept(ctx, "t-srv", pool, testServerID, "dbx")("LISTEN t_srv"); err != nil {
		t.Fatal(err)
	}
	closeListeners(testServerID, "other") // different database: untouched
	if listenerFor("t-srv") == nil {
		t.Fatal("listener of another database was closed")
	}
	closeListeners(testServerID, "dbx")
	if listenerFor("t-srv") != nil {
		t.Fatal("listener survived closeListeners")
	}
}
