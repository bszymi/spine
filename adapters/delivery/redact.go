package delivery

import (
	"errors"
	"net/url"
	"strconv"
	"strings"
)

// Webhook URL redaction (INIT-023 TASK-010, audit P3-26).
//
// Slack-, Discord- and many other webhook URLs carry their credential in
// the path or query (https://hooks.example/services/T/B/<secret>,
// ...?token=<secret>). Go's *url.Error renders the full request URL
// ("Post \"https://hooks.example/services/T/B/<secret>\": ..."), and that
// text used to be written verbatim to event_delivery_queue.last_error,
// event_delivery_log.error, the dispatcher's error log and the
// subscription-test response — readable by anyone with delivery-history
// or log access, kept in database dumps and backups, and (in SMP) shown
// to workspace members in the run page's dispatch-failure banner.
//
// Redaction happens at write time: logs cannot be redacted at read time.
// Scheme, host and port are kept so an operator can still tell which
// endpoint failed.

// redactedPath replaces any path, query or fragment.
const redactedPath = "/[redacted]"

// unparseableURL stands in for input url.Parse cannot read (or that has
// no host), so nothing of it is echoed.
const unparseableURL = "[unparseable-url]"

// RedactURL returns raw reduced to "scheme://host[:port]", plus
// "/[redacted]" when raw has any path (other than "/"), query or
// fragment. Userinfo is dropped. A bare "/" path is kept as is.
// Unparseable or host-less input gives "[unparseable-url]".
func RedactURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return unparseableURL
	}
	out := u.Scheme + "://" + u.Host
	hasPath := (u.Path != "" && u.Path != "/") || (u.RawPath != "" && u.RawPath != "/") || u.Opaque != ""
	switch {
	case hasPath || u.RawQuery != "" || u.Fragment != "" || u.RawFragment != "":
		out += redactedPath
	case u.Path == "/":
		out += "/"
	}
	return out
}

// RedactError renders err for an error column or a log line with every
// URL it carries reduced by RedactURL: each *url.Error in the chain
// (the request URL, or a redirect target) is re-rendered as
// `Op "RedactURL(URL)": <inner>`. As a backstop, the raw targetURL, its
// quoted form, its path, query and fragment are also replaced wherever
// they still appear in the text. A nil err gives "".
func RedactError(err error, targetURL string) string {
	if err == nil {
		return ""
	}
	msg := renderRedacted(err)
	// A *url.Error wrapped by another error was rendered by the
	// wrapper's Error(); redact its URL in place.
	for e := err; e != nil; e = errors.Unwrap(e) {
		if ue, ok := e.(*url.Error); ok && ue.URL != "" {
			msg = strings.ReplaceAll(msg, ue.URL, RedactURL(ue.URL))
		}
	}
	return scrubURL(msg, targetURL)
}

// renderRedacted re-renders a *url.Error chain with redacted URLs.
func renderRedacted(err error) string {
	ue, ok := err.(*url.Error)
	if !ok {
		return err.Error()
	}
	inner := "<nil>"
	if ue.Err != nil {
		inner = renderRedacted(ue.Err)
	}
	return ue.Op + " " + strconv.Quote(RedactURL(ue.URL)) + ": " + inner
}

// scrubURL replaces the secret-bearing parts of raw wherever they appear
// in msg. Over-redaction is harmless; a missed token is not.
func scrubURL(msg, raw string) string {
	if raw == "" {
		return msg
	}
	red := RedactURL(raw)
	msg = strings.ReplaceAll(msg, raw, red)
	if q := strconv.Quote(raw); len(q) > 2 {
		msg = strings.ReplaceAll(msg, q[1:len(q)-1], red)
	}
	u, err := url.Parse(raw)
	if err != nil {
		return msg
	}
	for _, part := range []string{u.EscapedPath(), u.Path, u.RawQuery, u.Query().Encode(), u.EscapedFragment(), u.Fragment} {
		if part != "" && part != "/" {
			msg = strings.ReplaceAll(msg, part, "[redacted]")
		}
	}
	return msg
}
