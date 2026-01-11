package rangecontrol

import (
	"errors"
	"reflect"
	"testing"
)

func TestParseContentRange(t *testing.T) {
	tests := []struct {
		name    string
		header  string
		want    *ContentRange
		wantErr error
	}{
		{
			name:   "Valid standard range with size",
			header: "bytes 0-99/1000",
			want: &ContentRange{
				Start: 0,
				End:   99,
				Size:  1000,
			},
		},
		{
			name:   "Valid standard range with unknown size",
			header: "bytes 200-299/*",
			want: &ContentRange{
				Start: 200,
				End:   299,
				Size:  -1,
			},
		},
		{
			name:   "Valid unsatisfied range",
			header: "bytes */1000",
			want: &ContentRange{
				Size:        1000,
				Unsatisfied: true,
			},
		},
		{
			name:   "Valid with whitespace",
			header: "  bytes 0-49/100  ",
			want: &ContentRange{
				Start: 0,
				End:   49,
				Size:  100,
			},
		},
		{
			name:    "Empty header",
			header:  "",
			wantErr: ErrInvalidContentRange,
		},
		{
			name:    "Missing space separator",
			header:  "bytes0-9/10",
			wantErr: ErrInvalidContentRange,
		},
		{
			name:    "Invalid unsatisfied size",
			header:  "bytes */abc",
			wantErr: ErrInvalidContentRange,
		},
		{
			name:    "Negative unsatisfied size",
			header:  "bytes */-1",
			wantErr: ErrInvalidContentRange,
		},
		{
			name:    "Missing slash",
			header:  "bytes 0-9 10",
			wantErr: ErrInvalidContentRange,
		},
		{
			name:    "Missing dash in range",
			header:  "bytes 0/10",
			wantErr: ErrInvalidContentRange,
		},
		{
			name:    "Invalid start position",
			header:  "bytes a-9/10",
			wantErr: ErrInvalidContentRange,
		},
		{
			name:    "Negative start position",
			header:  "bytes -1-9/10",
			wantErr: ErrInvalidContentRange,
		},
		{
			name:    "End before start",
			header:  "bytes 10-5/20",
			wantErr: ErrInvalidContentRange,
		},
		{
			name:    "End range equals or exceeds size",
			header:  "bytes 0-10/10",
			wantErr: ErrInvalidContentRange,
		},
		{
			name:    "Invalid size part",
			header:  "bytes 0-9/abc",
			wantErr: ErrInvalidContentRange,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseContentRange(tt.header)
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Errorf("ParseContentRange() error = %v, wantErr %v", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseContentRange() unexpected error: %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("ParseContentRange() = %+v, want %+v", got, tt.want)
			}
		})
	}
}
