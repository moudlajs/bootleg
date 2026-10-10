package matcher

import "testing"

func TestNormalize(t *testing.T) {
	tests := []struct{ in, want string }{
		{"Björk", "bjork"},
		{"BJORK!", "bjork"},
		{"Sigur Rós", "sigur ros"},
		{"Žlutý kůň úpěl ďábelské ódy", "zluty kun upel dabelske ody"},
		{"Røyksopp", "royksopp"},
		{"Mötley Crüe", "motley crue"},
		{"AC/DC", "ac dc"},
		{"  Glory   Box (Remastered) ", "glory box remastered"},
		{"Guns N' Roses", "guns n roses"},
		{"坂本龍一", "坂本龍一"},
		{"", ""},
		{"!!!", ""},
	}
	for _, tt := range tests {
		if got := Normalize(tt.in); got != tt.want {
			t.Errorf("Normalize(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestBest(t *testing.T) {
	tests := []struct {
		name       string
		artist     string
		title      string
		candidates []Candidate
		wantIndex  int
		wantOK     bool
	}{
		{
			name:       "exact match",
			artist:     "Portishead",
			title:      "Glory Box",
			candidates: []Candidate{{"Portishead", "Glory Box"}},
			wantIndex:  0, wantOK: true,
		},
		{
			name:       "no results",
			artist:     "Portishead",
			title:      "Glory Box",
			candidates: nil,
			wantIndex:  -1, wantOK: false,
		},
		{
			name:       "artist mismatch is rejected even with an exact title",
			artist:     "Portishead",
			title:      "Glory Box",
			candidates: []Candidate{{"Some Cover Band", "Glory Box"}},
			wantIndex:  -1, wantOK: false,
		},
		{
			name:       "title mismatch is rejected even with an exact artist",
			artist:     "Portishead",
			title:      "Glory Box",
			candidates: []Candidate{{"Portishead", "Roads"}},
			wantIndex:  -1, wantOK: false,
		},
		{
			name:   "karaoke result is rejected",
			artist: "Björk", title: "Army of Me",
			candidates: []Candidate{
				{"Karaoke Stars", "Army of Me (Karaoke Version)"},
				{"Björk", "Army of Me"},
			},
			wantIndex: 1, wantOK: true,
		},
		{
			name:   "tribute and made-famous-by results are rejected",
			artist: "Queen", title: "Bohemian Rhapsody",
			candidates: []Candidate{
				{"Queen Tribute Band", "Bohemian Rhapsody"},
				{"Queen", "Bohemian Rhapsody (Made Famous by Queen)"},
				{"Queen", "Bohemian Rhapsody (In the Style of Queen)"},
			},
			wantIndex: -1, wantOK: false,
		},
		{
			name:   "karaoke allowed when the query asks for it",
			artist: "Karaoke Stars", title: "Army of Me (Karaoke Version)",
			candidates: []Candidate{{"Karaoke Stars", "Army of Me (Karaoke Version)"}},
			wantIndex:  0, wantOK: true,
		},
		{
			name:       "diacritics: Bjork matches Björk",
			artist:     "Bjork",
			title:      "Army of Me",
			candidates: []Candidate{{"Björk", "Army of Me"}},
			wantIndex:  0, wantOK: true,
		},
		{
			name:       "diacritics: Björk matches Bjork",
			artist:     "Björk",
			title:      "Army of me",
			candidates: []Candidate{{"Bjork", "ARMY OF ME"}},
			wantIndex:  0, wantOK: true,
		},
		{
			name:   "tie keeps the API's order",
			artist: "Massive Attack", title: "Teardrop",
			candidates: []Candidate{
				{"Massive Attack", "Teardrop"},
				{"Massive Attack", "Teardrop"},
			},
			wantIndex: 0, wantOK: true,
		},
		{
			name:   "exact title beats a remastered variant listed first",
			artist: "Portishead", title: "Glory Box",
			candidates: []Candidate{
				{"Portishead", "Glory Box (Remastered)"},
				{"Portishead", "Glory Box"},
			},
			wantIndex: 1, wantOK: true,
		},
		{
			name:   "exact artist beats an exact title with a featured-artist credit",
			artist: "Björk", title: "Army of Me",
			candidates: []Candidate{
				{"Björk & Skunk Anansie", "Army of Me"},
				{"Björk", "Army of Me (Live)"},
			},
			wantIndex: 1, wantOK: true,
		},
		{
			name:       "partial artist credit is still accepted",
			artist:     "Björk",
			title:      "Army of Me",
			candidates: []Candidate{{"Björk & Skunk Anansie", "Army of Me"}},
			wantIndex:  0, wantOK: true,
		},
		{
			name:       "word boundaries: 'me' does not match 'home'",
			artist:     "Björk",
			title:      "Me",
			candidates: []Candidate{{"Björk", "Home"}},
			wantIndex:  -1, wantOK: false,
		},
		{
			name:   "whole term: artist in the term scores higher",
			artist: "", title: "teardrop massive attack",
			candidates: []Candidate{
				{"Elizabeth Fraser", "Teardrop"},
				{"Massive Attack", "Teardrop"},
			},
			wantIndex: 1, wantOK: true,
		},
		{
			name:   "whole term: title must appear in the term",
			artist: "", title: "teardrop massive attack",
			candidates: []Candidate{{"Massive Attack", "Angel"}},
			wantIndex:  -1, wantOK: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotIndex, gotOK := Best(tt.artist, tt.title, tt.candidates)
			if gotIndex != tt.wantIndex || gotOK != tt.wantOK {
				t.Errorf("Best() = (%d, %v), want (%d, %v)", gotIndex, gotOK, tt.wantIndex, tt.wantOK)
			}
		})
	}
}

func TestRelated(t *testing.T) {
	tests := []struct {
		artist, title string
		c             Candidate
		want          bool
	}{
		{"Radiohead", "Kreep", Candidate{"Radiohead", "Creep"}, true},          // artist shared
		{"Portished", "Glory Box", Candidate{"Portishead", "Glory Box"}, true}, // title shared
		{"Radiohead", "Kreep", Candidate{"Coldplay", "Yellow"}, false},         // unrelated
		{"Bjork", "Army of Me", Candidate{"Karaoke Stars", "Army of Me"}, false},
		{"Karaoke Stars", "Army of Me (Karaoke)", Candidate{"Karaoke Stars", "Army of Me (Karaoke)"}, true},
		{"", "teardrop masive atack", Candidate{"Massive Attack", "Teardrop"}, true}, // whole term
		{"", "teardrop masive atack", Candidate{"Coldplay", "Yellow"}, false},
	}
	for _, tt := range tests {
		if got := Related(tt.artist, tt.title, tt.c); got != tt.want {
			t.Errorf("Related(%q, %q, %+v) = %v, want %v", tt.artist, tt.title, tt.c, got, tt.want)
		}
	}
}
