package rangecontrol

import (
	"reflect"
	"testing"
)

func TestParse(t *testing.T) {
	tests := []struct {
		name    string
		header  string
		want    []ByteRange
		wantErr bool
	}{
		{"empty header", "", nil, false},
		{"valid single closed", "bytes=0-499", []ByteRange{{Start: 0, End: 499}}, false},
		{"valid multiple", "bytes=0-0, 1-1", []ByteRange{{Start: 0, End: 0}, {Start: 1, End: 1}}, false},
		{"open-ended", "bytes=100-", []ByteRange{{Start: 100, End: -1}}, false},
		{"spaces and multiple", "bytes=0-10,20-30", []ByteRange{{Start: 0, End: 10}, {Start: 20, End: 30}}, false},
		{"invalid prefix", "byt=0-1", nil, true},
		{"suffix-range not supported", "bytes=-500", nil, true},
		{"no dash", "bytes=500", nil, true},
		{"non-numeric start", "bytes=a-5", nil, true},
		{"end less than start", "bytes=10-5", nil, true},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			got, err := Parse(tc.header)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error but got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %#v, want %#v", got, tc.want)
			}
		})
	}
}

func TestSortRanges(t *testing.T) {
	tests := []struct {
		name string
		in   []ByteRange
		want []ByteRange
	}{
		{
			"sort by start",
			[]ByteRange{{Start: 100, End: 200}, {Start: 0, End: 50}},
			[]ByteRange{{Start: 0, End: 50}, {Start: 100, End: 200}},
		},
		{
			"same start, closed first",
			[]ByteRange{{Start: 0, End: -1}, {Start: 0, End: 50}},
			[]ByteRange{{Start: 0, End: 50}, {Start: 0, End: -1}},
		},
		{
			"same start, smaller end first",
			[]ByteRange{{Start: 0, End: 100}, {Start: 0, End: 50}},
			[]ByteRange{{Start: 0, End: 50}, {Start: 0, End: 100}},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			SortRanges(tc.in)
			if !reflect.DeepEqual(tc.in, tc.want) {
				t.Fatalf("got %#v, want %#v", tc.in, tc.want)
			}
		})
	}
}

func TestMergeRanges(t *testing.T) {
	tests := []struct {
		name string
		in   []ByteRange
		want []ByteRange
	}{
		{"nil input", nil, nil},
		{"empty input", []ByteRange{}, []ByteRange{}},
		{
			"no overlap",
			[]ByteRange{{Start: 0, End: 10}, {Start: 20, End: 30}},
			[]ByteRange{{Start: 0, End: 10}, {Start: 20, End: 30}},
		},
		{
			"adjacent",
			[]ByteRange{{Start: 0, End: 10}, {Start: 11, End: 20}},
			[]ByteRange{{Start: 0, End: 20}},
		},
		{
			"overlapping",
			[]ByteRange{{Start: 0, End: 10}, {Start: 5, End: 15}},
			[]ByteRange{{Start: 0, End: 15}},
		},
		{
			"open-ended absorbs consecutive",
			[]ByteRange{{Start: 0, End: -1}, {Start: 5, End: 15}, {Start: 20, End: 30}},
			[]ByteRange{{Start: 0, End: -1}},
		},
		{
			"merge into open-ended",
			[]ByteRange{{Start: 0, End: 10}, {Start: 5, End: -1}},
			[]ByteRange{{Start: 0, End: -1}},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := MergeRanges(tc.in)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %#v, want %#v", got, tc.want)
			}
		})
	}
}

func TestByteRange_ContentRange(t *testing.T) {
	tests := []struct {
		name      string
		byteRange ByteRange
		totalSize int64
		want      string
	}{
		{
			"total size zero or negative",
			ByteRange{Start: 0, End: 100},
			0,
			"bytes 0-100/*",
		},
		{
			"open-ended range",
			ByteRange{Start: 100, End: -1},
			1000,
			"bytes 100-999/1000",
		},
		{
			"end greater than total size",
			ByteRange{Start: 0, End: 2000},
			1000,
			"bytes 0-999/1000",
		},
		{
			"normal closed range",
			ByteRange{Start: 0, End: 499},
			1000,
			"bytes 0-499/1000",
		},
		{
			"exactly at total size",
			ByteRange{Start: 0, End: 999},
			1000,
			"bytes 0-999/1000",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.byteRange.ContentRange(tc.totalSize)
			if got != tc.want {
				t.Errorf("ContentRange(%d) = %v, want %v", tc.totalSize, got, tc.want)
			}
		})
	}
}
