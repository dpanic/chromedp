package chromedp

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chromedp/cdproto"
	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/cdproto/runtime"
)

// The tests in this file cover WithoutRuntimeEnable. The first ones use the
// browser. The last ones use a target with a fake browser.

// TestWithoutRuntimeEnableQueries runs queries and polls in the top document,
// in a same-origin iframe and after a navigation, with and without the option.
func TestWithoutRuntimeEnableQueries(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name string
		opts []ContextOption
		// global is the type of a global of the page, as Poll sees it.
		global string
	}{
		{"Default", nil, "string"},
		{"WithoutRuntimeEnable", []ContextOption{WithoutRuntimeEnable()}, "undefined"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			ctx, cancel := testAllocate(t, "world.html", test.opts...)
			defer cancel()
			ctx, tcancel := context.WithTimeout(ctx, 30*time.Second)
			defer tcancel()

			if text, err := Run(ctx, Text(ID("top"))); err != nil || text != "top doc" {
				t.Fatalf("got %q and %v for the text of the top document, want %q", text, err, "top doc")
			}
			frames, err := Run(ctx, Nodes(ID("frame")))
			if err != nil {
				t.Fatal(err)
			}
			if text, err := Run(ctx, Text(CSS("#child1 p"), FromNode(frames[0]))); err != nil || text != "child one" {
				t.Fatalf("got %q and %v for the text in the iframe, want %q", text, err, "child one")
			}
			if got, err := Run(ctx, Poll[string](`document.querySelector("#child1 p").textContent`, WithPollingInFrame(frames[0]))); err != nil || got != "child one" {
				t.Fatalf("got %q and %v from Poll in the iframe, want %q", got, err, "child one")
			}
			if got, err := Run(ctx, Poll[string](`document.querySelector("#top").textContent`)); err != nil || got != "top doc" {
				t.Fatalf("got %q and %v from Poll in the top document, want %q", got, err, "top doc")
			}

			// Poll runs in an isolated world without Runtime.enable, so it
			// does not see the globals of the page. Evaluate always does.
			if got, err := Run(ctx, Poll[string](`typeof window.pageValue`)); err != nil || got != test.global {
				t.Fatalf("got %q and %v from Poll for the type of a page global, want %q", got, err, test.global)
			}
			if got, err := Run(ctx, Evaluate[string](`window.pageValue`)); err != nil || got != "main world" {
				t.Fatalf("got %q and %v from Evaluate for a page global, want %q", got, err, "main world")
			}

			// A world of the old document fails with "Cannot find context
			// with specified id", so the target must drop it.
			if err := Do(ctx, Navigate(testdataDir+"/child2.html")); err != nil {
				t.Fatal(err)
			}
			if text, err := Run(ctx, Text(CSS("#child2 p"))); err != nil || text != "child two" {
				t.Fatalf("got %q and %v for the text after the navigation, want %q", text, err, "child two")
			}
			if got, err := Run(ctx, Poll[string](`document.querySelector("#child2 p").textContent`)); err != nil || got != "child two" {
				t.Fatalf("got %q and %v from Poll after the navigation, want %q", got, err, "child two")
			}
		})
	}
}

// waitWorld waits until the target keeps an isolated world for the frame, when
// want is true, or keeps none, when want is false. It returns the world. The
// target handles the frame events on its own goroutine, so the test must wait.
func waitWorld(t *testing.T, ctx context.Context, frame cdp.FrameID, want bool) runtime.ExecutionContextID {
	t.Helper()
	tgt := FromContext(ctx).Target
	for {
		tgt.frameMu.RLock()
		world := tgt.execContexts[frame]
		tgt.frameMu.RUnlock()
		if (world != 0) == want {
			return world
		}
		if err := sleepContext(ctx, 10*time.Millisecond); err != nil {
			t.Fatalf("frame %s has the world %d, and the wait for a change ended: %v", frame, world, err)
		}
	}
}

// TestWithoutRuntimeEnableFrameWorldDropped makes sure that the target drops
// the isolated world of an iframe when the iframe navigates and when it
// detaches.
func TestWithoutRuntimeEnableFrameWorldDropped(t *testing.T) {
	t.Parallel()

	ctx, cancel := testAllocate(t, "world.html", WithoutRuntimeEnable())
	defer cancel()
	ctx, tcancel := context.WithTimeout(ctx, 30*time.Second)
	defer tcancel()

	frames, err := Run(ctx, Nodes(ID("frame")))
	if err != nil {
		t.Fatal(err)
	}
	child := frames[0].FrameID
	if got, err := Run(ctx, Poll[string](`document.querySelector("#child1 p").textContent`, WithPollingInFrame(frames[0]))); err != nil || got != "child one" {
		t.Fatalf("got %q and %v from Poll in the iframe, want %q", got, err, "child one")
	}
	first := waitWorld(t, ctx, child, true)

	if _, err := Run(ctx, Evaluate[Void](`document.getElementById("frame").src = "child2.html"`)); err != nil {
		t.Fatal(err)
	}
	waitWorld(t, ctx, child, false)
	if got, err := Run(ctx, Poll[string](`document.querySelector("#child2 p").textContent`, WithPollingInFrame(frames[0]))); err != nil || got != "child two" {
		t.Fatalf("got %q and %v from Poll in the iframe after its navigation, want %q", got, err, "child two")
	}
	if second := waitWorld(t, ctx, child, true); second == first {
		t.Fatalf("the iframe has the world %d of its old document after the navigation", second)
	}

	if _, err := Run(ctx, Evaluate[Void](`document.getElementById("frame").remove()`)); err != nil {
		t.Fatal(err)
	}
	waitWorld(t, ctx, child, false)
	if text, err := Run(ctx, Text(ID("top"))); err != nil || text != "top doc" {
		t.Fatalf("got %q and %v for the text of the top document, want %q", text, err, "top doc")
	}
}

// TestWithoutRuntimeEnableProbe loads a page that finds out whether the
// Runtime domain is enabled. The page finds it by default, and not with the
// option.
func TestWithoutRuntimeEnableProbe(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name string
		opts []ContextOption
		want string
	}{
		{"Default", nil, "detected"},
		{"WithoutRuntimeEnable", []ContextOption{WithoutRuntimeEnable()}, "not detected"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			ctx, cancel := testAllocate(t, "runtimeleak.html", test.opts...)
			defer cancel()
			ctx, tcancel := context.WithTimeout(ctx, 30*time.Second)
			defer tcancel()

			got, err := Run(ctx, Text(ID("result")))
			if err != nil {
				t.Fatal(err)
			}
			if got != test.want {
				t.Fatalf("the page says %q, want %q", got, test.want)
			}
		})
	}
}

// TestWithoutRuntimeEnableEvents makes sure that the console, exception and
// binding events arrive by default, and not with the option.
func TestWithoutRuntimeEnableEvents(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name string
		opts []ContextOption
		want bool
		// kind is the type of the binding in the page.
		kind string
	}{
		{"Default", nil, true, "function"},
		{"WithoutRuntimeEnable", []ContextOption{WithoutRuntimeEnable()}, false, "undefined"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			ctx, cancel := testAllocate(t, "", test.opts...)
			defer cancel()
			ctx, tcancel := context.WithTimeout(ctx, 30*time.Second)
			defer tcancel()

			var (
				mu                          sync.Mutex
				console, exception, binding bool
			)
			seen := func() (bool, bool, bool) {
				mu.Lock()
				defer mu.Unlock()
				return console, exception, binding
			}
			if err := Listen(ctx, func(ev any) {
				mu.Lock()
				defer mu.Unlock()
				switch ev := ev.(type) {
				case *runtime.EventConsoleAPICalled:
					for _, arg := range ev.Args {
						console = console || strings.Contains(string(arg.Value), "hello console")
					}
				case *runtime.EventExceptionThrown:
					exception = exception || strings.Contains(ev.ExceptionDetails.Exception.Description, "uncaught exception")
				case *runtime.EventBindingCalled:
					binding = binding || (ev.Name == "probeBinding" && ev.Payload == "ping")
				}
			}); err != nil {
				t.Fatal(err)
			}
			if _, err := Call(ctx, runtime.AddBinding, runtime.AddBindingParams{Name: "probeBinding"}); err != nil {
				t.Fatal(err)
			}
			if err := Do(ctx, Navigate(testdataDir+"/runtimeevents.html")); err != nil {
				t.Fatal(err)
			}
			// The last script of the page writes #done. The events of the
			// scripts before it arrive before the reply that finds #done.
			kind, err := Run(ctx, Text(ID("done")))
			if err != nil {
				t.Fatal(err)
			}
			if kind != test.kind {
				t.Fatalf("the binding has the type %q in the page, want %q", kind, test.kind)
			}
			c, e, b := seen()
			// Make sure that a late event is not the reason of a failure.
			for test.want && !(c && e && b) {
				if err := sleepContext(ctx, 10*time.Millisecond); err != nil {
					break
				}
				c, e, b = seen()
			}
			if c != test.want || e != test.want || b != test.want {
				t.Fatalf("got the console event %v, the exception event %v and the binding event %v, want %v", c, e, b, test.want)
			}
		})
	}
}

// fakeClock is a clock that moves only when the test moves it. Each read
// sends to reads, so that a test can count the turns of a loop that reads it.
type fakeClock struct {
	mu    sync.Mutex
	now   time.Time
	reads chan struct{}
}

func newFakeClock() *fakeClock {
	return &fakeClock{now: time.Unix(1, 0), reads: make(chan struct{}, 1024)}
}

func (c *fakeClock) read() time.Time {
	select {
	case c.reads <- struct{}{}:
	default:
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) add(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// fakeTarget is a target without Runtime.enable. Its top frame has a document,
// and a fake browser answers its commands. It counts the Page.createIsolatedWorld
// commands and keeps the messages that errf logs.
type fakeTarget struct {
	*Target
	clock *fakeClock
	// worlds gets a value for each Page.createIsolatedWorld, before the reply.
	worlds chan struct{}
	calls  atomic.Int32

	mu     sync.Mutex
	logged []string
}

func (f *fakeTarget) logs() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.logged)
}

// refuseWorld fails every command, as Chrome fails for a frame that cannot get
// a world.
func refuseWorld(_ *fakeTarget, msg *cdproto.Message) *cdproto.Message {
	return &cdproto.Message{ID: msg.ID, Error: &cdproto.Error{Code: -32000, Message: "Could not create isolated world"}}
}

// newFakeTarget makes a fakeTarget. answer gives the reply to each command.
func newFakeTarget(t *testing.T, answer func(*fakeTarget, *cdproto.Message) *cdproto.Message) *fakeTarget {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	b := &Browser{cmdQueue: make(chan *cdproto.Message), LostConnection: make(chan struct{})}
	f := &fakeTarget{
		clock:  newFakeClock(),
		worlds: make(chan struct{}, 1024),
	}
	f.Target = &Target{
		browser:         b,
		detached:        make(chan struct{}),
		frames:          map[cdp.FrameID]*Frame{"top": {Frame: &cdp.Frame{ID: "top"}, Root: &Node{}}},
		execContexts:    map[cdp.FrameID]runtime.ExecutionContextID{},
		execUniqueIDs:   map[cdp.FrameID]string{},
		cur:             "top",
		noRuntimeEnable: true,
		now:             f.clock.read,
		logf:            func(string, ...any) {},
		errf: func(format string, args ...any) {
			f.mu.Lock()
			defer f.mu.Unlock()
			f.logged = append(f.logged, fmt.Sprintf(format, args...))
		},
	}
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case msg := <-b.cmdQueue:
				if msg.Method == page.CommandCreateIsolatedWorld {
					f.calls.Add(1)
					f.worlds <- struct{}{}
				}
				reply := answer(f, msg)
				f.listenersMu.Lock()
				listeners := slices.Clone(f.listeners)
				f.listenersMu.Unlock()
				for _, l := range listeners {
					if l.ctx.Err() == nil {
						l.fn(reply)
					}
				}
			}
		}
	}()
	return f
}

// waitFor waits for a value on ch. It fails the test when the value does not
// come. The limit only matters when the test fails.
func waitFor(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(30 * time.Second):
		t.Fatalf("waited too long for %s", what)
	}
}

// A query and a poll in a frame that cannot get its isolated world wait, as
// they wait for a context with Runtime.enable. They send Page.createIsolatedWorld
// again only after worldRetryInterval, and not every 5 ms. When their context
// ends, they log the last error and return the error of the context itself.
func TestWithoutRuntimeEnableRefusedWorld(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name string
		run  func(context.Context, *Target) error
	}{
		{"Query", func(ctx context.Context, t *Target) error {
			_, err := OuterHTML(CSS("html"))(ctx, t)
			return err
		}},
		{"Poll", func(ctx context.Context, t *Target) error {
			_, err := Poll[bool]("true")(ctx, t)
			return err
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			f := newFakeTarget(t, refuseWorld)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			errc := make(chan error, 1)
			go func() { errc <- test.run(ctx, f.Target) }()

			waitFor(t, f.worlds, "the first Page.createIsolatedWorld")
			// The loop reads the clock in each turn while it remembers the
			// failure. The clock does not move, so the loop must not send the
			// command again.
			for turns := 0; turns < 10; {
				select {
				case <-f.clock.reads:
					turns++
				case <-f.worlds:
					t.Fatalf("Page.createIsolatedWorld was sent again after %d turns, before the retry interval ended", turns)
				case <-time.After(30 * time.Second):
					t.Fatal("waited too long for the turns of the loop")
				}
			}
			f.clock.add(worldRetryInterval)
			waitFor(t, f.worlds, "the Page.createIsolatedWorld after the retry interval")

			cancel()
			if err := <-errc; err != context.Canceled {
				t.Fatalf("got the error %v, want context.Canceled itself", err)
			}
			if n := f.calls.Load(); n != 2 {
				t.Errorf("Page.createIsolatedWorld was sent %d times, want 2", n)
			}
			if logs := f.logs(); len(logs) != 1 || !strings.Contains(logs[0], "Could not create isolated world") {
				t.Errorf("got the logs %q, want one line with the error of the world", logs)
			}
		})
	}
}

// A drop of the worlds also drops the remembered failure, so the next call
// sends the command at once.
func TestWithoutRuntimeEnableFailureDropped(t *testing.T) {
	t.Parallel()

	f := newFakeTarget(t, refuseWorld)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if _, err := f.frameExecContext(ctx, "top"); err == nil || f.calls.Load() != 1 {
		t.Fatalf("first call: got the error %v and %d commands, want an error and 1 command", err, f.calls.Load())
	}
	var e *cdproto.Error
	if _, err := f.frameExecContext(ctx, "top"); !errors.As(err, &e) || f.calls.Load() != 1 {
		t.Fatalf("call in the interval: got the error %v and %d commands, want the remembered error and 1 command", err, f.calls.Load())
	}
	f.clock.add(worldRetryInterval)
	if _, err := f.frameExecContext(ctx, "top"); err == nil || f.calls.Load() != 2 {
		t.Fatalf("call after the interval: got the error %v and %d commands, want an error and 2 commands", err, f.calls.Load())
	}
	f.dropWorlds("top")
	if _, err := f.frameExecContext(ctx, "top"); err == nil || f.calls.Load() != 3 {
		t.Fatalf("call after a drop: got the error %v and %d commands, want an error and 3 commands", err, f.calls.Load())
	}
}

// A navigation between Page.createIsolatedWorld and its reply drops the worlds.
// The reply can name a world of the old document, so the target uses it once
// and does not keep it.
func TestWithoutRuntimeEnableWorldReplyAfterDrop(t *testing.T) {
	t.Parallel()

	f := newFakeTarget(t, func(f *fakeTarget, msg *cdproto.Message) *cdproto.Message {
		// Page.frameNavigated arrives before the reply.
		f.dropWorlds("top")
		return &cdproto.Message{ID: msg.ID, Result: []byte(`{"executionContextId":7}`)}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if id, err := f.frameExecContext(ctx, "top"); err != nil || id != 7 {
		t.Fatalf("got the world %d and the error %v, want 7", id, err)
	}
	f.frameMu.RLock()
	kept := f.execContexts["top"]
	f.frameMu.RUnlock()
	if kept != 0 {
		t.Fatalf("the target keeps the world %d of the old document", kept)
	}
	if _, err := f.frameExecContext(ctx, "top"); err != nil || f.calls.Load() != 2 {
		t.Fatalf("next call: got the error %v and %d commands, want a new command", err, f.calls.Load())
	}
}

// With Runtime.enable, the target never sends Page.createIsolatedWorld, and the
// frame events do not drop the contexts that the Runtime events reported.
func TestWithRuntimeEnableNoWorld(t *testing.T) {
	t.Parallel()

	f := newFakeTarget(t, refuseWorld)
	f.noRuntimeEnable = false
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if id, err := f.frameExecContext(ctx, "top"); err != nil || id != 0 || f.calls.Load() != 0 {
		t.Fatalf("got the context %d, the error %v and %d commands, want 0, no error and no command", id, err, f.calls.Load())
	}
	f.frameMu.Lock()
	f.execContexts["top"] = 3
	f.frameMu.Unlock()
	f.dropWorlds("top")
	f.dropWorlds("")
	if id, err := f.frameExecContext(ctx, "top"); err != nil || id != 3 || f.calls.Load() != 0 {
		t.Fatalf("got the context %d, the error %v and %d commands, want 3, no error and no command", id, err, f.calls.Load())
	}
}
