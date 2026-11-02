package tests

import (
	"bufio"
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

var httpClientNoJar = http.Client{}

func newReq(e *env, method, path string) (*http.Request, error) {
	return http.NewRequest(method, e.srv.URL+path, nil)
}

func ctxTimeout(t *testing.T, d time.Duration) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), d)
	t.Cleanup(cancel)
	return ctx
}

// readSSEUntil membaca stream sampai event bernama `name` yang datanya memuat `contains`.
func readSSEUntil(t *testing.T, r io.Reader, name, contains string) string {
	sc := bufio.NewScanner(r)
	var ev string
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "event: "):
			ev = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			if ev == name && strings.Contains(line, contains) {
				return line
			}
		}
	}
	return ""
}
