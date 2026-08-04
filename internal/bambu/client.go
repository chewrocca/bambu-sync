package bambu

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// DefaultBaseURL is the Bambu Cloud REST API root. The /v1 is mandatory —
// without it every path returns a clean JSON 404 rather than an error that
// tells you why.
const DefaultBaseURL = "https://api.bambulab.com/v1"

// ErrTokenExpired is returned on any 401. The token lasts ~3 months and the
// refresh endpoint returns only 401s, so this always means a manual re-auth.
// It is deliberately distinguishable: it is the one failure that needs a human
// rather than a retry.
var ErrTokenExpired = errors.New("bambu: token expired or revoked (401)")

// Client is a read-only Bambu Cloud API client.
//
// A plain net/http client is sufficient. api.bambulab.com sits behind
// Cloudflare, and the mature Python clients ship browser-impersonation
// workarounds (curl_cffi, cloudscraper) — but those are not needed for
// token-authenticated reads. Verified 2026-08-03: Go's default
// "Go-http-client/2.0" User-Agent passes cleanly, cf-mitigated empty.
type Client struct {
	BaseURL string
	HTTP    *http.Client

	// token is supplied per-call rather than stored, because the process is
	// long-lived and the token rotates roughly quarterly. See config.Load.
}

// New returns a Client with sane timeouts.
func New(baseURL string) *Client {
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	return &Client{
		BaseURL: baseURL,
		HTTP:    &http.Client{Timeout: 30 * time.Second},
	}
}

// get performs an authenticated GET and decodes JSON into out.
func (c *Client) get(ctx context.Context, token, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+path, nil)
	if err != nil {
		return fmt.Errorf("bambu: build request for %s: %w", path, err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("bambu: GET %s: %w", path, err)
	}
	defer func() { _ = resp.Body.Close() }()

	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusUnauthorized:
		return ErrTokenExpired
	default:
		return fmt.Errorf("bambu: GET %s: unexpected status %d", path, resp.StatusCode)
	}

	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("bambu: decode %s: %w", path, err)
	}
	return nil
}

// Tasks fetches print history, newest first. limit=100 returns everything at
// current volume; the API pages by offset if history ever outgrows that.
func (c *Client) Tasks(ctx context.Context, token string, limit int) (*TasksResponse, error) {
	var out TasksResponse
	err := c.get(ctx, token, "/user-service/my/tasks?limit="+strconv.Itoa(limit), &out)
	return &out, err
}

// Filament fetches RFID-registered spools. Only rolls loaded into the AMS
// appear — sealed spares are invisible to this endpoint.
func (c *Client) Filament(ctx context.Context, token string) (*FilamentResponse, error) {
	var out FilamentResponse
	err := c.get(ctx, token, "/design-user-service/my/filament/v2", &out)
	return &out, err
}

// Favorites fetches MakerWorld favourites. Two calls: the uid must be resolved
// first, because the favourites path is uid-scoped.
func (c *Client) Favorites(ctx context.Context, token string, limit int) (*FavoritesResponse, error) {
	var pref PreferenceResponse
	if err := c.get(ctx, token, "/design-user-service/my/preference", &pref); err != nil {
		return nil, err
	}
	q := url.Values{"offset": {"0"}, "limit": {strconv.Itoa(limit)}}
	path := fmt.Sprintf("/design-service/favorites/designs/%d?%s", pref.UID, q.Encode())

	var out FavoritesResponse
	err := c.get(ctx, token, path, &out)
	return &out, err
}

// Devices fetches the account's bound printers.
//
// Cheap (one call, one device for most accounts) and the only source of an
// accurate model name — see the BindResponse doc comment.
func (c *Client) Devices(ctx context.Context, token string) (*BindResponse, error) {
	var out BindResponse
	err := c.get(ctx, token, "/iot-service/api/user/bind", &out)
	return &out, err
}
