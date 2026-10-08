// Package parser turns an input file of "Artist - Title" lines into queries.
package parser

import (
	"bufio"
	"fmt"
	"io"
	"strings"
)

// separator splits artist from title; only the first one counts ("Song - Remastered").
const separator = " - "

// bom is the UTF-8 byte order mark Windows Notepad puts at the start of a file.
const bom = "\uFEFF"

// Query is one song to look up.
type Query struct {
	Artist string // empty when the line had no separator
	Title  string // the whole line when Artist is empty
	Raw    string // the trimmed line as written, for the unmatched report
	Line   int    // 1-based line number in the input file
}

// Term is the search string sent to the API.
func (q Query) Term() string {
	if q.Artist == "" {
		return q.Title
	}
	return q.Artist + " " + q.Title
}

// Parse reads queries from r, skipping (and counting) blank and # comment lines.
func Parse(r io.Reader) (queries []Query, skipped int, err error) {

	// ScanLines already drops a trailing \r, so CRLF needs nothing extra.
	sc := bufio.NewScanner(r)
	for n := 1; sc.Scan(); n++ {
		line := sc.Text()
		if n == 1 {
			line = strings.TrimPrefix(line, bom)
		}
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			skipped++
			continue
		}

		q := Query{Raw: line, Line: n, Title: line}
		if artist, title, ok := strings.Cut(line, separator); ok {
			q.Artist = strings.TrimSpace(artist)
			q.Title = strings.TrimSpace(title)
		}
		queries = append(queries, q)
	}
	if err := sc.Err(); err != nil {
		return nil, 0, fmt.Errorf("read input: %w", err)
	}
	return queries, skipped, nil
}
