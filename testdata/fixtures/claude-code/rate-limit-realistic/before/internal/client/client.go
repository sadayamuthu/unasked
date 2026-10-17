package client

import "net/http"

// Do sends req, forwarding the caller's request id for tracing.
func Do(req *http.Request, id string) (*http.Response, error) {
	req.Header.Set("X-Request-Id", id)
	return http.DefaultClient.Do(req)
}
