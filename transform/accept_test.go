package transform

import (
	"errors"
	"net/http"
	"strings"
	"testing"
)

func TestAcceptSpecificityWeightsAndStablePriority(t *testing.T) {
	for _, test := range []struct {
		name, header, want string
		explicit           string
		missing            bool
		err                error
	}{
		{name: "missing preserves source", missing: true, want: "image/jpeg"},
		{name: "wildcard preserves source", header: "*/*", want: "image/jpeg"},
		{name: "unrelated exact plus wildcard", header: "application/json, */*", want: "image/jpeg"},
		{name: "type wildcard priority", header: "image/*", want: "image/webp"},
		{name: "specific q0 wins", header: "image/webp;q=0,image/*;q=1", want: "image/jpeg"},
		{name: "specific lower weight", header: "image/webp;q=0.5,image/*;q=1", want: "image/jpeg"},
		{name: "higher exact weight", header: "image/jpeg;q=0.8,image/webp", want: "image/webp"},
		{name: "tie request order ignored", header: "image/jpeg,image/webp", want: "image/webp"},
		{name: "tie reverse", header: "image/webp,image/jpeg", want: "image/webp"},
		{name: "conflicting duplicates", header: "image/webp;q=0,image/webp;q=1,image/jpeg", want: "image/jpeg"},
		{name: "all rejected", header: "*/*;q=0", err: ErrNotAcceptable},
		{name: "empty is not missing", header: "", err: ErrNotAcceptable},
		{name: "unsupported requested type", header: "image/avif", err: ErrNotAcceptable},
		{name: "required parameter absent", header: "image/webp;profile=special", err: ErrNotAcceptable},
		{name: "explicit source rejected", header: "image/webp", explicit: "source", err: ErrNotAcceptable},
		{name: "explicit png", header: "image/png,image/jpeg", explicit: "png", want: "image/png"},
		{name: "explicit invalid format", header: "*/*", explicit: "avif", err: ErrInvalidParameter},
		{name: "explicit q0 not bypassed", header: "image/webp;q=0,*/*", explicit: "webp", err: ErrNotAcceptable},
	} {
		t.Run(test.name, func(t *testing.T) {
			header := http.Header{"Accept": {test.header}}
			if test.missing {
				header = nil
			}
			a, err := ParseAccept(header)
			if err != nil {
				t.Fatal(err)
			}
			got, err := a.Choose("image/jpeg", []string{"image/webp", "image/jpeg", "image/png"}, test.explicit)
			if got != test.want || !errors.Is(err, test.err) {
				t.Fatal(got, err)
			}
		})
	}
}
func TestAcceptParametersAndFieldLines(t *testing.T) {
	a, err := ParseAccept(http.Header{"Accept": {"text/*;q=0.3, text/html;q=0.7, text/html;level=1", `text/html;level="2";q=0.4, */*;q=0.5`}, "accept": {"image/webp;q=0.123"}})
	if err != nil {
		t.Fatal(err)
	}
	for media, want := range map[string]int{"text/html;level=1": 1000, "text/html": 700, "text/html;level=2": 400, "image/jpeg": 500, "image/webp": 123} {
		if got := a.Quality(media); got != want {
			t.Fatal(media, got, want)
		}
	}
	a, err = ParseAccept(http.Header{"Accept": {`image/webp;profile="a,b;c";q=0.7, image/webp;q=0.1`}})
	if err != nil || a.Quality(`image/webp;profile="a,b;c"`) != 700 || a.Quality("image/webp") != 100 {
		t.Fatal(a, err)
	}
}
func TestAcceptRejectsMalformedOrUnboundedInput(t *testing.T) {
	for _, header := range []string{"image/webp;q=.8", "image/webp;q=1.001", "image/webp;q=0.1234", "image/webp;q=-1", "image/webp;q=NaN", "image/webp;q=1e0", `image/webp;q="0.8"`, "image/webp;q=0;q=0", `image/webp;profile="unclosed`, "*/png", "image", "image/", "image/webp\r\nX: y", strings.Repeat("image/webp,", 129), strings.Repeat(" ", maxAcceptBytes+1)} {
		if _, err := ParseAccept(http.Header{"Accept": {header}}); !errors.Is(err, ErrInvalidAccept) {
			t.Fatalf("accepted %q: %v", header, err)
		}
	}
	for raw, want := range map[string]int{"0.": 0, "1.": 1000, "0.001": 1, "0.05": 50, "1.000": 1000} {
		got, err := parseQuality(raw)
		if err != nil || got != want {
			t.Fatal(raw, got, err)
		}
	}
}
