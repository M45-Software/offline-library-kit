package main

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"
	"unicode"
)

const maxDownloadHistory = 50

type downloadHistoryEntry struct {
	ID         string    `json:"id,omitempty"`
	Title      string    `json:"title"`
	State      string    `json:"state"`
	Time       time.Time `json:"time"`
	Retries    int       `json:"retries"`
	HTTPStatus int       `json:"httpStatus,omitempty"`
	Detail     string    `json:"detail,omitempty"`
}
type httpDownloadError struct{ status int }

func (e *httpDownloadError) Error() string { return fmt.Sprintf("Download returned HTTP %d", e.status) }

var historyURLs = regexp.MustCompile(`(?i)https?://\S+`)

func historyDetail(err error) string {
	if err == nil {
		return "Verified and prepared"
	}
	var u *url.Error
	if errors.As(err, &u) {
		err = u.Err
	}
	var path *os.PathError
	if errors.As(err, &path) {
		err = fmt.Errorf("%s: %w", path.Op, path.Err)
	}
	s := historyURLs.ReplaceAllString(err.Error(), "[URL omitted]")
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s)
	runes := []rune(s)
	if len(runes) > 200 {
		s = string(runes[:200]) + "…"
	}
	return s
}

// Caller holds app.mu. Keeping only terminal outcomes avoids a write per chunk.
func (a *app) appendHistoryLocked(id, title string, err error, retries, status int) {
	if !uuidPattern.MatchString(id) {
		id = ""
	}
	if title == "" {
		title = "Download"
	}
	runes := []rune(title)
	if len(runes) > 200 {
		title = string(runes[:200])
	}
	state := "complete"
	if err != nil {
		state = "failed"
	}
	entry := downloadHistoryEntry{ID: id, Title: title, State: state, Time: time.Now().UTC(), Retries: retries, HTTPStatus: status, Detail: historyDetail(err)}
	a.downloadHistory = append([]downloadHistoryEntry{entry}, a.downloadHistory...)
	if len(a.downloadHistory) > maxDownloadHistory {
		a.downloadHistory = a.downloadHistory[:maxDownloadHistory]
	}
}
