package bambu

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// recorder captures what the client actually sent, so tests assert on the
// request rather than trusting the implementation.
type recorder struct {
	mu    sync.Mutex
	paths []string
	auth  []string
}

func (r *recorder) note(req *http.Request) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.paths = append(r.paths, req.URL.Path+"?"+req.URL.RawQuery)
	r.auth = append(r.auth, req.Header.Get("Authorization"))
}

// serve stands up a fake API. handler is keyed on path prefix so a test can
// script a multi-call flow.
func serve(t *testing.T, rec *recorder, routes map[string]func(w http.ResponseWriter)) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		rec.note(req)
		for prefix, h := range routes {
			if strings.HasPrefix(req.URL.Path, "/v1"+prefix) {
				h(w)
				return
			}
		}
		http.Error(w, `{"error":"no route"}`, http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)
	c := New(srv.URL + "/v1")
	return c
}

func ok(body string) func(http.ResponseWriter) {
	return func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}
}

func status(code int) func(http.ResponseWriter) {
	return func(w http.ResponseWriter) { w.WriteHeader(code) }
}

// Every request must carry the bearer token. A silent omission would look like
// an API change rather than a client bug.
func TestRequestsCarryBearerToken(t *testing.T) {
	rec := &recorder{}
	c := serve(t, rec, map[string]func(http.ResponseWriter){
		"/user-service/my/tasks": ok(`{"hits":[]}`),
	})
	if _, err := c.Tasks(context.Background(), "TOK123", 10); err != nil {
		t.Fatal(err)
	}
	if got := rec.auth[0]; got != "Bearer TOK123" {
		t.Errorf("Authorization = %q, want %q", got, "Bearer TOK123")
	}
}

// A 401 is the one failure that needs a human: the token lasts ~3 months and
// the refresh endpoint returns only 401s. It must be distinguishable with
// errors.Is so callers can alert on it specifically rather than string-match.
func TestUnauthorizedIsDistinguishable(t *testing.T) {
	rec := &recorder{}
	c := serve(t, rec, map[string]func(http.ResponseWriter){
		"/user-service/my/tasks": status(http.StatusUnauthorized),
	})
	_, err := c.Tasks(context.Background(), "expired", 10)
	if !errors.Is(err, ErrTokenExpired) {
		t.Fatalf("401 must map to ErrTokenExpired, got %v", err)
	}
}

// Anything else is a transient/unknown failure and must NOT be mistaken for an
// expired token -- that would send a "re-authenticate" alert for an outage.
func TestOtherStatusesAreNotTokenExpiry(t *testing.T) {
	for _, code := range []int{http.StatusInternalServerError, http.StatusBadGateway,
		http.StatusTooManyRequests, http.StatusForbidden, http.StatusNotFound} {
		rec := &recorder{}
		c := serve(t, rec, map[string]func(http.ResponseWriter){
			"/user-service/my/tasks": status(code),
		})
		_, err := c.Tasks(context.Background(), "tok", 10)
		if err == nil {
			t.Errorf("status %d: want an error", code)
			continue
		}
		if errors.Is(err, ErrTokenExpired) {
			t.Errorf("status %d must not be reported as token expiry", code)
		}
	}
}

// The API is unofficial and can change shape without notice. A malformed body
// must be an error, never a silently-empty result that publishes zeroes over
// good metrics.
func TestMalformedBodyIsAnError(t *testing.T) {
	rec := &recorder{}
	c := serve(t, rec, map[string]func(http.ResponseWriter){
		"/user-service/my/tasks": ok(`{"hits": [ this is not json`),
	})
	if _, err := c.Tasks(context.Background(), "tok", 10); err == nil {
		t.Error("want an error for a malformed body")
	}
}

// Favourites is a TWO-call flow: the uid must be resolved first, because the
// favourites path is uid-scoped. Assert the order and that the uid is
// interpolated, not hardcoded.
func TestFavoritesResolvesUIDFirst(t *testing.T) {
	rec := &recorder{}
	c := serve(t, rec, map[string]func(http.ResponseWriter){
		"/design-user-service/my/preference": ok(`{"uid":424242}`),
		"/design-service/favorites/designs/": ok(`{"hits":[{"id":7,"title":"x","printCount":3}]}`),
	})

	got, err := c.Favorites(context.Background(), "tok", 25)
	if err != nil {
		t.Fatal(err)
	}
	if len(rec.paths) != 2 {
		t.Fatalf("want 2 calls (preference then favourites), got %d: %v", len(rec.paths), rec.paths)
	}
	if !strings.Contains(rec.paths[0], "/my/preference") {
		t.Errorf("first call should resolve the uid, got %q", rec.paths[0])
	}
	if !strings.Contains(rec.paths[1], "/favorites/designs/424242") {
		t.Errorf("uid not interpolated into the favourites path: %q", rec.paths[1])
	}
	if !strings.Contains(rec.paths[1], "limit=25") {
		t.Errorf("limit not passed: %q", rec.paths[1])
	}
	if len(got.Hits) != 1 || got.Hits[0].PrintCount != 3 {
		t.Errorf("unexpected decode: %+v", got.Hits)
	}
}

// A failure resolving the uid must abort rather than fetching favourites for
// uid 0 and quietly returning someone else's list (or nothing).
func TestFavoritesAbortsIfUIDLookupFails(t *testing.T) {
	rec := &recorder{}
	c := serve(t, rec, map[string]func(http.ResponseWriter){
		"/design-user-service/my/preference": status(http.StatusUnauthorized),
	})
	if _, err := c.Favorites(context.Background(), "tok", 25); !errors.Is(err, ErrTokenExpired) {
		t.Fatalf("want ErrTokenExpired from the uid lookup, got %v", err)
	}
	if len(rec.paths) != 1 {
		t.Errorf("must not call favourites after a failed uid lookup, got %v", rec.paths)
	}
}

// The /v1 prefix is mandatory -- without it every path returns a clean JSON
// 404, which reads like "no data" rather than "wrong URL". Cost an hour once.
func TestBaseURLDefaultsToV1(t *testing.T) {
	if got := New("").BaseURL; got != DefaultBaseURL {
		t.Errorf("New(\"\").BaseURL = %q, want %q", got, DefaultBaseURL)
	}
	if !strings.HasSuffix(DefaultBaseURL, "/v1") {
		t.Errorf("DefaultBaseURL must end in /v1, got %q", DefaultBaseURL)
	}
}

func TestTasksPassesLimit(t *testing.T) {
	rec := &recorder{}
	c := serve(t, rec, map[string]func(http.ResponseWriter){
		"/user-service/my/tasks": ok(`{"hits":[]}`),
	})
	if _, err := c.Tasks(context.Background(), "tok", 1); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(rec.paths[0], "limit=1") {
		t.Errorf("limit not passed: %q", rec.paths[0])
	}
}

// A cancelled context must abort rather than hang -- the sync runs under a
// timeout and must not outlive it.
func TestContextCancellationAborts(t *testing.T) {
	rec := &recorder{}
	c := serve(t, rec, map[string]func(http.ResponseWriter){
		"/user-service/my/tasks": ok(`{"hits":[]}`),
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.Tasks(ctx, "tok", 10); err == nil {
		t.Error("want an error for a cancelled context")
	}
}

// slotId arrives as a number when a spool is loaded and "" otherwise. Both
// must decode -- `any` is deliberate, and a typed int would break on "".
func TestFilamentDecodesBothSlotShapes(t *testing.T) {
	rec := &recorder{}
	c := serve(t, rec, map[string]func(http.ResponseWriter){
		"/design-user-service/my/filament/v2": ok(
			`{"hits":[{"filamentName":"PLA","slotId":3,"inPrinter":true},
			          {"filamentName":"PETG","slotId":"","inPrinter":false}]}`),
	})
	got, err := c.Filament(context.Background(), "tok")
	if err != nil {
		t.Fatalf("both slotId shapes must decode: %v", err)
	}
	if len(got.Hits) != 2 {
		t.Fatalf("want 2 spools, got %d", len(got.Hits))
	}
}
