package parser

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestParse(t *testing.T) {
	tests := []struct {
		name        string
		input       string
		want        []Query
		wantSkipped int
	}{
		{
			name:  "artist and title",
			input: "Portishead - Glory Box\n",
			want:  []Query{{Artist: "Portishead", Title: "Glory Box", Raw: "Portishead - Glory Box", Line: 1}},
		},
		{
			name:  "splits on first separator only",
			input: "Queen - Bohemian Rhapsody - Remastered 2011",
			want:  []Query{{Artist: "Queen", Title: "Bohemian Rhapsody - Remastered 2011", Raw: "Queen - Bohemian Rhapsody - Remastered 2011", Line: 1}},
		},
		{
			name:  "hyphen without spaces is not a separator",
			input: "Jay-Z - 99 Problems",
			want:  []Query{{Artist: "Jay-Z", Title: "99 Problems", Raw: "Jay-Z - 99 Problems", Line: 1}},
		},
		{
			name:        "comments and blank lines are skipped, line numbers kept",
			input:       "# my list\n\n   \nMassive Attack - Teardrop\n  # indented comment\n",
			want:        []Query{{Artist: "Massive Attack", Title: "Teardrop", Raw: "Massive Attack - Teardrop", Line: 4}},
			wantSkipped: 4,
		},
		{
			name:  "missing separator is a whole-term query",
			input: "teardrop massive attack",
			want:  []Query{{Title: "teardrop massive attack", Raw: "teardrop massive attack", Line: 1}},
		},
		{
			// TrimSpace runs before Cut, so an edge " - " is no longer a separator.
			name:  "separator at the edges is not a split",
			input: " - Title\nArtist - \n",
			want: []Query{
				{Title: "- Title", Raw: "- Title", Line: 1},
				{Title: "Artist -", Raw: "Artist -", Line: 2},
			},
		},
		{
			name:  "trailing whitespace and CRLF",
			input: "Björk - Army of Me  \t\r\nSigur Rós - Hoppípolla\r\n",
			want: []Query{
				{Artist: "Björk", Title: "Army of Me", Raw: "Björk - Army of Me", Line: 1},
				{Artist: "Sigur Rós", Title: "Hoppípolla", Raw: "Sigur Rós - Hoppípolla", Line: 2},
			},
		},
		{
			name:  "UTF-8 non-Latin",
			input: "坂本龍一 - Merry Christmas Mr. Lawrence",
			want:  []Query{{Artist: "坂本龍一", Title: "Merry Christmas Mr. Lawrence", Raw: "坂本龍一 - Merry Christmas Mr. Lawrence", Line: 1}},
		},
		{
			name:  "BOM is stripped",
			input: "\uFEFFRadiohead - Airbag\n",
			want:  []Query{{Artist: "Radiohead", Title: "Airbag", Raw: "Radiohead - Airbag", Line: 1}},
		},
		{
			name:        "BOM before a comment still makes it a comment",
			input:       "\uFEFF# header\nRadiohead - Airbag",
			want:        []Query{{Artist: "Radiohead", Title: "Airbag", Raw: "Radiohead - Airbag", Line: 2}},
			wantSkipped: 1,
		},
		{
			name:  "empty input",
			input: "",
			want:  nil,
		},
		{
			name:        "only comments",
			input:       "# a\n# b\n",
			want:        nil,
			wantSkipped: 2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, skipped, err := Parse(strings.NewReader(tt.input))
			if err != nil {
				t.Fatalf("Parse() error = %v", err)
			}
			if skipped != tt.wantSkipped {
				t.Errorf("skipped = %d, want %d", skipped, tt.wantSkipped)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("Parse() =\n  %#v\nwant\n  %#v", got, tt.want)
			}
		})
	}
}

func TestQueryTerm(t *testing.T) {
	tests := []struct {
		q    Query
		want string
	}{
		{Query{Artist: "Björk", Title: "Army of Me"}, "Björk Army of Me"},
		{Query{Title: "teardrop massive attack"}, "teardrop massive attack"},
	}
	for _, tt := range tests {
		if got := tt.q.Term(); got != tt.want {
			t.Errorf("Term() = %q, want %q", got, tt.want)
		}
	}
}

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, errors.New("disk on fire") }

func TestParseReadError(t *testing.T) {
	_, _, err := Parse(failingReader{})
	if err == nil || !strings.Contains(err.Error(), "disk on fire") {
		t.Fatalf("Parse() error = %v, want wrapped read error", err)
	}
}
