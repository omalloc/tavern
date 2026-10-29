package varycontrol

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func TestParseVary(t *testing.T) {
	cases := []struct {
		name     string
		values   []string
		want     Key
		wildcard bool
		err      error
	}{
		{"empty", []string{"", ", ,\t"}, nil, false, nil},
		{"case and duplicates", []string{"Accept, ACCEPT-Encoding", "accept, X-Custom"}, Key{"accept", "accept-encoding", "x-custom"}, false, nil},
		{"wildcard alone", []string{"*"}, nil, true, nil},
		{"wildcard mixed", []string{"Accept, *", "X-Foo"}, Key{"accept", "x-foo"}, true, nil},
		{"space", []string{"bad field"}, nil, false, ErrInvalidField},
		{"colon", []string{"Accept:"}, nil, false, ErrInvalidField},
		{"newline", []string{"Accept\r\n"}, nil, false, ErrInvalidField},
		{"non ascii", []string{"X-图片"}, nil, false, ErrInvalidField},
		{"token punctuation", []string{"!#$%&'*+-.^_`|~"}, Key{"!#$%&'*+-.^_`|~"}, false, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, wildcard, err := Parse(tc.values...)
			if !reflect.DeepEqual(got, tc.want) || wildcard != tc.wildcard || !errors.Is(err, tc.err) {
				t.Fatalf("Parse = %v, %t, %v; want %v, %t, %v", got, wildcard, err, tc.want, tc.wildcard, tc.err)
			}
		})
	}
}

func TestParseVaryBudget(t *testing.T) {
	var fields []string
	for i := range MaxFields {
		fields = append(fields, fmt.Sprintf("x-%d", i))
	}
	if got, _, err := Parse(strings.Join(fields, ",")); err != nil || len(got) != MaxFields {
		t.Fatalf("budget boundary: %v, %v", got, err)
	}
	if _, _, err := Parse(strings.Join(fields, ","), "x-extra"); !errors.Is(err, ErrTooManyFields) {
		t.Fatalf("expected field budget error, got %v", err)
	}
	if got, _, err := Parse(strings.Join(fields, ","), "X-0"); err != nil || len(got) != MaxFields {
		t.Fatalf("duplicate consumed budget: %v, %v", got, err)
	}
}

func FuzzParseVary(f *testing.F) {
	for _, seed := range []string{"", "Accept,Accept-Encoding", "Accept,*", "X-Foo\r\n", "a=b&c=d"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, value string) {
		first, star1, err1 := Parse(value)
		second, star2, err2 := Parse(value)
		if !reflect.DeepEqual(first, second) || star1 != star2 || !errors.Is(err1, err2) {
			t.Fatal("nondeterministic parse")
		}
		if err1 == nil {
			canonical, _, err := Parse(first...)
			if err != nil || !reflect.DeepEqual(canonical, first) {
				t.Fatal("noncanonical field set")
			}
		}
	})
}
