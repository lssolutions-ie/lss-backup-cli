package healthchecks

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	DefaultDomain = "https://cron.lssolutions.ie"
	timeout       = 10 * time.Second
)

// Config holds the connection details for a healthchecks.io compatible instance.
// PingURL returns the base ping URL: {Domain}/ping/{ID}
// Append "/start" or "/fail" for those events; no suffix means success.
type Config struct {
	Domain string // e.g. "https://cron.lssolutions.ie"
	ID     string // UUID from the healthchecks dashboard
}

func (c Config) pingURL() string {
	return strings.TrimRight(c.Domain, "/") + "/ping/" + c.ID
}

// PingStart signals to healthchecks that the job has started.
// If the job then fails to call PingSuccess or PingFail within the grace period,
// healthchecks will mark it as missed — the dead man's switch.
func PingStart(c Config, log io.Writer) {
	send(c.pingURL()+"/start", "", log)
}

// PingSuccess signals that the job completed without errors.
func PingSuccess(c Config, log io.Writer) {
	send(c.pingURL(), "", log)
}

// PingFail signals that the job failed. errMsg is sent as the request body so
// it appears in the healthchecks dashboard — limit is 10 KB.
func PingFail(c Config, errMsg string, log io.Writer) {
	body := errMsg
	if len(body) > 10_000 {
		body = body[:10_000]
	}
	send(c.pingURL()+"/fail", body, log)
}

// send fires an HTTP request and logs a warning on failure.
// It never returns an error — a failed ping must never block or fail a backup job.
func send(url string, body string, log io.Writer) {
	client := &http.Client{Timeout: timeout}

	var (
		resp *http.Response
		err  error
	)

	if body != "" {
		resp, err = client.Post(url, "text/plain", strings.NewReader(body))
	} else {
		resp, err = client.Get(url)
	}

	if err != nil {
		fmt.Fprintf(log, "Warning: healthchecks ping failed (%s): %v\n", url, err)
		return
	}
	resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		fmt.Fprintf(log, "Warning: healthchecks ping returned unexpected status %d (%s)\n", resp.StatusCode, url)
	}
}
