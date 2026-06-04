package varycontrol

import (
	"net/http"
	"testing"
)

func TestClean(t *testing.T) {
	tests := []struct {
		name   string
		values []string
		want   Key
	}{
		{
			name:   "single value",
			values: []string{"Accept-Encoding"},
			want:   Key{"Accept-Encoding"},
		},
		{
			name:   "comma separated",
			values: []string{"Accept-Encoding, User-Agent"},
			want:   Key{"Accept-Encoding", "User-Agent"},
		},
		{
			name:   "multiple values with overlap",
			values: []string{"Accept-Encoding", "User-Agent, Accept-Encoding"},
			want:   Key{"Accept-Encoding", "User-Agent"},
		},
		{
			name:   "empty string ignored",
			values: []string{"", "Accept-Encoding"},
			want:   Key{"Accept-Encoding"},
		},
		{
			name:   "just comma ignored",
			values: []string{","},
			want:   nil,
		},
		{
			name:   "whitespace trimmed",
			values: []string{"  Accept-Encoding ,  User-Agent  "},
			want:   Key{"Accept-Encoding", "User-Agent"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Clean(tt.values...)
			if len(got) != len(tt.want) {
				t.Fatalf("Clean() = %v (len=%d), want %v (len=%d)", got, len(got), tt.want, len(tt.want))
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Fatalf("Clean()[%d] = %q, want %q", i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestAppend(t *testing.T) {
	t.Run("append to empty", func(t *testing.T) {
		var k Key
		k.Append("Accept-Encoding")
		if len(k) != 1 || k[0] != "Accept-Encoding" {
			t.Fatalf("got %v", k)
		}
	})

	t.Run("append duplicate is skipped", func(t *testing.T) {
		k := Key{"Accept-Encoding", "User-Agent"}
		k.Append("Accept-Encoding")
		if len(k) != 2 {
			t.Fatalf("duplicate not skipped: %v", k)
		}
	})

	t.Run("append comma-separated with existing entries", func(t *testing.T) {
		k := Key{"Accept-Encoding"}
		// "User-Agent, Accept-Encoding" — Accept-Encoding already present, User-Agent is new
		k.Append("User-Agent, Accept-Encoding")
		if len(k) != 2 {
			t.Fatalf("got len=%d: %v", len(k), k)
		}
		if k[0] != "Accept-Encoding" || k[1] != "User-Agent" {
			t.Fatalf("wrong order or content: %v", k)
		}
	})

	t.Run("append stays sorted", func(t *testing.T) {
		k := Key{"Accept-Encoding"}
		k.Append("User-Agent, Cookie")
		if k[0] != "Accept-Encoding" || k[1] != "Cookie" || k[2] != "User-Agent" {
			t.Fatalf("not sorted: %v", k)
		}
	})

	t.Run("append empty", func(t *testing.T) {
		k := Key{"Accept-Encoding"}
		k.Append("")
		if len(k) != 1 {
			t.Fatalf("empty append changed key: %v", k)
		}
	})
}

func TestVaryData(t *testing.T) {
	tests := []struct {
		name    string
		key     Key
		headers http.Header
		want    string
	}{
		{
			name:    "empty key",
			key:     Key{},
			headers: http.Header{"Accept-Encoding": {"gzip"}},
			want:    "",
		},
		{
			name:    "single header present",
			key:     Key{"Accept-Encoding"},
			headers: http.Header{"Accept-Encoding": {"gzip"}},
			want:    "Accept-Encoding=gzip",
		},
		{
			name:    "single header missing — stable identity",
			key:     Key{"User-Agent"},
			headers: http.Header{"Accept-Encoding": {"gzip"}},
			want:    "User-Agent=" + VaryEmptyIdentity,
		},
		{
			name:    "multiple headers sorted",
			key:     Key{"Accept-Encoding", "User-Agent"},
			headers: http.Header{
				"Accept-Encoding": {"gzip"},
				"User-Agent":      {"test-agent"},
			},
			want: "Accept-Encoding=gzip&User-Agent=test-agent",
		},
		{
			name:    "accept-encoding normalized with quality factors",
			key:     Key{"Accept-Encoding"},
			headers: http.Header{"Accept-Encoding": {"gzip;q=0.8, br;q=1.0"}},
			want:    "Accept-Encoding=br,gzip",
		},
		{
			name:    "accept-encoding identity removed",
			key:     Key{"Accept-Encoding"},
			headers: http.Header{"Accept-Encoding": {"gzip, identity"}},
			want:    "Accept-Encoding=gzip",
		},
		{
			name:    "accept-encoding all identity becomes sentinel",
			key:     Key{"Accept-Encoding"},
			headers: http.Header{"Accept-Encoding": {"identity"}},
			want:    "Accept-Encoding=" + VaryEmptyIdentity,
		},
		{
			name:    "multi-value header sorted",
			key:     Key{"Accept-Encoding"},
			headers: http.Header{"Accept-Encoding": {"gzip", "br"}},
			want:    "Accept-Encoding=br,gzip",
		},
		{
			name: "deterministic order regardless of header order",
			key:  Key{"Accept-Encoding", "User-Agent"},
			headers: http.Header{
				"User-Agent":      {"test-agent"},
				"Accept-Encoding": {"gzip"},
			},
			want: "Accept-Encoding=gzip&User-Agent=test-agent",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.key.VaryData(tt.headers)
			if got != tt.want {
				t.Errorf("VaryData() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestVaryData_Deterministic(t *testing.T) {
	// Same headers, same Key → same output.
	key := Key{"Accept-Encoding", "User-Agent"}
	h1 := http.Header{
		"Accept-Encoding": {"gzip, br"},
		"User-Agent":      {"curl/7.0"},
	}
	h2 := http.Header{
		"User-Agent":      {"curl/7.0"},
		"Accept-Encoding": {"br, gzip"},
	}

	got1 := key.VaryData(h1)
	got2 := key.VaryData(h2)
	if got1 != got2 {
		t.Errorf("not deterministic:\n  got1 = %q\n  got2 = %q", got1, got2)
	}
}

func TestFilterIgnore(t *testing.T) {
	t.Run("empty ignore", func(t *testing.T) {
		k := Key{"Accept-Encoding", "User-Agent"}
		got := k.FilterIgnore(map[string]struct{}{})
		if len(got) != 2 {
			t.Fatalf("got %v", got)
		}
	})

	t.Run("ignore one", func(t *testing.T) {
		k := Key{"Accept-Encoding", "User-Agent", "Cookie"}
		got := k.FilterIgnore(map[string]struct{}{"Cookie": {}})
		if len(got) != 2 || got[0] != "Accept-Encoding" || got[1] != "User-Agent" {
			t.Fatalf("got %v", got)
		}
	})

	t.Run("ignore all", func(t *testing.T) {
		k := Key{"Accept-Encoding"}
		got := k.FilterIgnore(map[string]struct{}{"Accept-Encoding": {}})
		if len(got) != 0 {
			t.Fatalf("got %v", got)
		}
	})

	t.Run("preserves sort order", func(t *testing.T) {
		k := Key{"Accept-Encoding", "Cookie", "User-Agent"}
		got := k.FilterIgnore(map[string]struct{}{"Cookie": {}})
		if len(got) != 2 || got[0] != "Accept-Encoding" || got[1] != "User-Agent" {
			t.Fatalf("got %v", got)
		}
	})

	t.Run("nil key", func(t *testing.T) {
		var k Key
		got := k.FilterIgnore(map[string]struct{}{"Cookie": {}})
		if got != nil {
			t.Fatalf("got %v", got)
		}
	})
}

func TestCompare(t *testing.T) {
	tests := []struct {
		name string
		k1   Key
		k2   Key
		want bool
	}{
		{"equal", Key{"A", "B"}, Key{"A", "B"}, true},
		{"different length", Key{"A"}, Key{"A", "B"}, false},
		{"different order", Key{"A", "B"}, Key{"B", "A"}, false},
		{"different values", Key{"A", "B"}, Key{"A", "C"}, false},
		{"both nil", nil, nil, true},
		{"one nil", nil, Key{"A"}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.k1.Compare(tt.k2); got != tt.want {
				t.Errorf("Compare() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestString(t *testing.T) {
	k := Key{"Accept-Encoding", "User-Agent"}
	if s := k.String(); s != "Accept-Encoding,User-Agent" {
		t.Errorf("String() = %q", s)
	}
}

func TestSortValue(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"", ""},
		{"  ", ""},
		{"gzip", "gzip"},
		{"br, gzip", "br,gzip"},
		{"gzip, deflate, br", "br,deflate,gzip"},
		{",", ""},
	}

	for _, tt := range tests {
		got := sortValue(tt.input)
		if got != tt.want {
			t.Errorf("sortValue(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestNormalizeAcceptEncoding(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"", ""},
		{"gzip", "gzip"},
		{"gzip, br", "br,gzip"},
		{"br, gzip, deflate", "br,deflate,gzip"},
		{"gzip;q=0.8, br;q=1.0, deflate", "br,deflate,gzip"},
		{"gzip;q=0.8", "gzip"},
		{"gzip, identity", "gzip"},
		{"identity", ""},
		{"identity, identity", ""},
		{"  gzip ,  br  ", "br,gzip"},
	}

	for _, tt := range tests {
		got := normalizeAcceptEncoding(tt.input)
		if got != tt.want {
			t.Errorf("normalizeAcceptEncoding(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func BenchmarkVaryData(b *testing.B) {
	key := Key{"Accept-Encoding", "User-Agent", "Accept-Language"}
	headers := http.Header{
		"Accept-Encoding": {"gzip, br"},
		"User-Agent":      {"Mozilla/5.0"},
		"Accept-Language": {"en-US,en;q=0.9"},
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = key.VaryData(headers)
	}
}

func BenchmarkClean(b *testing.B) {
	vals := []string{"Accept-Encoding, User-Agent", "Accept-Language, Cookie"}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = Clean(vals...)
	}
}

func BenchmarkNormalizeAcceptEncoding(b *testing.B) {
	b.Run("no quality factors", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			_ = normalizeAcceptEncoding("gzip, deflate, br")
		}
	})
	b.Run("with quality factors", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			_ = normalizeAcceptEncoding("gzip;q=0.8, br;q=1.0, deflate")
		}
	})
}
