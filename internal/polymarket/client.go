package polymarket

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"strings"
	"time"

	"rn1-strategy-extractor/internal/util"
)

type Client struct {
	gammaBase  string
	dataBase   string
	clobBase   string
	httpClient *http.Client
	gammaLimit *util.TokenBucket
	dataLimit  *util.TokenBucket
	clobLimit  *util.TokenBucket
	retryMax   int
	userAgent  string
}

type ClientOptions struct {
	GammaBaseURL string
	DataBaseURL  string
	ClobBaseURL  string
	HTTPTimeout  time.Duration
	GammaRPS     float64
	DataRPS      float64
	ClobRPS      float64
	RetryMax     int
	UserAgent    string
}

func NewClient(opts ClientOptions) *Client {
	if opts.UserAgent == "" {
		opts.UserAgent = "rn1-strategy-extractor/0.1 (read-only public ingestion)"
	}
	if opts.GammaRPS <= 0 {
		opts.GammaRPS = 5
	}
	if opts.DataRPS <= 0 {
		opts.DataRPS = 3
	}
	if opts.ClobRPS <= 0 {
		opts.ClobRPS = 5
	}
	if opts.HTTPTimeout <= 0 {
		opts.HTTPTimeout = 30 * time.Second
	}
	if opts.RetryMax <= 0 {
		opts.RetryMax = 5
	}
	if opts.ClobBaseURL == "" {
		opts.ClobBaseURL = "https://clob.polymarket.com"
	}
	return &Client{
		gammaBase:  strings.TrimRight(opts.GammaBaseURL, "/"),
		dataBase:   strings.TrimRight(opts.DataBaseURL, "/"),
		clobBase:   strings.TrimRight(opts.ClobBaseURL, "/"),
		httpClient: &http.Client{Timeout: opts.HTTPTimeout},
		gammaLimit: util.NewTokenBucket(opts.GammaRPS, int(opts.GammaRPS)+1),
		dataLimit:  util.NewTokenBucket(opts.DataRPS, int(opts.DataRPS)+1),
		clobLimit:  util.NewTokenBucket(opts.ClobRPS, int(opts.ClobRPS)+1),
		retryMax:   opts.RetryMax,
		userAgent:  opts.UserAgent,
	}
}

func (c *Client) Close() {
	if c == nil {
		return
	}
	c.gammaLimit.Close()
	c.dataLimit.Close()
	c.clobLimit.Close()
}

func (c *Client) GammaBase() string { return c.gammaBase }
func (c *Client) DataBase() string  { return c.dataBase }
func (c *Client) ClobBase() string  { return c.clobBase }

type RawResponse struct {
	URL        string
	StatusCode int
	Body       []byte
	Hash       string
	FetchedAt  time.Time
}

func (c *Client) getJSON(ctx context.Context, host string, fullURL string) (*RawResponse, error) {
	var limiter *util.TokenBucket
	switch host {
	case "gamma":
		limiter = c.gammaLimit
	case "data":
		limiter = c.dataLimit
	case "clob":
		limiter = c.clobLimit
	default:
		return nil, fmt.Errorf("unknown host bucket: %s", host)
	}

	var lastErr error
	for attempt := 0; attempt <= c.retryMax; attempt++ {
		if err := limiter.Wait(ctx); err != nil {
			return nil, err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, fullURL, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Accept", "application/json")
		req.Header.Set("User-Agent", c.userAgent)

		resp, err := c.httpClient.Do(req)
		if err != nil {
			lastErr = err
			sleepBackoff(ctx, attempt)
			continue
		}
		body, readErr := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if readErr != nil {
			lastErr = readErr
			sleepBackoff(ctx, attempt)
			continue
		}
		if shouldRetry(resp.StatusCode) {
			lastErr = fmt.Errorf("http %d: %s", resp.StatusCode, truncate(body, 256))
			sleepBackoff(ctx, attempt)
			continue
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return nil, fmt.Errorf("http %d: %s", resp.StatusCode, truncate(body, 256))
		}
		return &RawResponse{
			URL:        fullURL,
			StatusCode: resp.StatusCode,
			Body:       body,
			Hash:       util.SHA256Bytes(body),
			FetchedAt:  time.Now().UTC(),
		}, nil
	}
	return nil, fmt.Errorf("gave up after %d retries: %w", c.retryMax, lastErr)
}

func shouldRetry(code int) bool {
	switch code {
	// 408 is added based on live behavior: Polymarket /activity occasionally
	// returns 408 "Request timed out. Please try again." for dense windows.
	case 408, 429, 500, 502, 503, 504:
		return true
	}
	return false
}

func sleepBackoff(ctx context.Context, attempt int) {
	base := time.Duration(1<<attempt) * 500 * time.Millisecond
	if base > 30*time.Second {
		base = 30 * time.Second
	}
	jitter := randomJitter(base / 2)
	select {
	case <-ctx.Done():
	case <-time.After(base + jitter):
	}
}

func randomJitter(max time.Duration) time.Duration {
	if max <= 0 {
		return 0
	}
	n, err := rand.Int(rand.Reader, big.NewInt(int64(max)))
	if err != nil {
		return max / 2
	}
	return time.Duration(n.Int64())
}

func truncate(b []byte, n int) string {
	if len(b) <= n {
		return string(b)
	}
	return string(b[:n]) + "...(truncated)"
}

func buildURL(base, path string, params map[string]string) string {
	u, err := url.Parse(base + path)
	if err != nil {
		return base + path
	}
	if len(params) > 0 {
		q := u.Query()
		for k, v := range params {
			if v == "" {
				continue
			}
			q.Set(k, v)
		}
		u.RawQuery = q.Encode()
	}
	return u.String()
}

func DecodeJSON[T any](body []byte) (T, error) {
	var t T
	dec := json.NewDecoder(strings.NewReader(string(body)))
	dec.UseNumber()
	if err := dec.Decode(&t); err != nil {
		return t, err
	}
	return t, nil
}
