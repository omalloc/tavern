package rangecontrol

import "testing"

var (
	benchmarkByteRanges         []ByteRange
	benchmarkContentRangeString string
)

func BenchmarkParseRange(b *testing.B) {
	cases := []struct {
		name  string
		value string
		size  int64
	}{
		{"single", "bytes=0-499", 1 << 20},
		{"open_ended", "bytes=500-", 1 << 20},
		{"suffix", "bytes=-500", 1 << 20},
		{"multi", "bytes=0-499,500-999,-500", 1 << 20},
		{"spaced_multi", "bytes=0-0, 1-1, 1048570-", 1 << 20},
	}

	for _, tc := range cases {
		b.Run(tc.name, func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				ranges, err := ParseRange(tc.value, tc.size)
				if err != nil {
					b.Fatal(err)
				}
				benchmarkByteRanges = ranges
			}
		})
	}
}

func BenchmarkParse(b *testing.B) {
	cases := []struct {
		name  string
		value string
		size  uint64
	}{
		{"single", "bytes=0-499", 1 << 20},
		{"open_ended", "bytes=500-", 1 << 20},
		{"suffix", "bytes=-500", 1 << 20},
		{"multi", "bytes=0-499,500-999,-500", 1 << 20},
		{"spaced_multi", "bytes=0-0, 1-1, 1048570-", 1 << 20},
	}

	for _, tc := range cases {
		b.Run(tc.name, func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				ranges, err := Parse(tc.value, tc.size)
				if err != nil {
					b.Fatal(err)
				}
				benchmarkByteRanges = ranges
			}
		})
	}
}

func BenchmarkContentRange(b *testing.B) {
	cases := []struct {
		name string
		r    ByteRange
		size uint64
	}{
		{"closed", ByteRange{Start: 0, End: 499}, 1 << 20},
		{"open_ended", ByteRange{Start: 500, End: -1}, 1 << 20},
		{"clamped", ByteRange{Start: 0, End: 2 << 20}, 1 << 20},
		{"unknown_size", ByteRange{Start: 0, End: 499}, 0},
	}

	for _, tc := range cases {
		b.Run(tc.name, func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				benchmarkContentRangeString = tc.r.ContentRange(tc.size)
			}
		})
	}
}
