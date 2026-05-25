package health

import (
	"fmt"
	"net/http"
	"time"
)

type Result struct {
	OK         bool    `json:"ok"`
	StatusCode *int    `json:"status_code"`
	Error      *string `json:"error"`
	DurationMs int64   `json:"duration_ms"`
}

func Check(url string, timeout time.Duration) Result {
	start := time.Now()
	client := &http.Client{Timeout: timeout}

	resp, err := client.Get(url)
	elapsed := time.Since(start).Milliseconds()

	if err != nil {
		msg := err.Error()
		return Result{OK: false, Error: &msg, DurationMs: elapsed}
	}
	defer resp.Body.Close()

	code := resp.StatusCode
	ok := code >= 200 && code < 400

	errMsg := ""
	if !ok {
		errMsg = fmt.Sprintf("HTTP %d", code)
	}

	var e *string
	if errMsg != "" {
		e = &errMsg
	}

	return Result{OK: ok, StatusCode: &code, Error: e, DurationMs: elapsed}
}
