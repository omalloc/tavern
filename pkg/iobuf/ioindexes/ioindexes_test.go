package ioindexes_test

import (
	"reflect"
	"testing"

	"github.com/omalloc/tavern/pkg/iobuf/ioindexes"
)

const BlockSize = 1024 * 1024

func TestBuild(t *testing.T) {
	type args struct {
		start    uint64
		end      uint64
		partSize uint64
	}
	tests := []struct {
		name string
		args args
		want []uint32
	}{
		{
			name: "single part",
			args: args{start: 0, end: 10, partSize: BlockSize},
			want: []uint32{0},
		},
		{
			name: "two parts",
			args: args{start: 0, end: BlockSize*2 - 1, partSize: BlockSize},
			want: []uint32{0, 1},
		},
		{
			name: "start offset",
			args: args{start: 1048576, end: BlockSize*2 + 1, partSize: BlockSize},
			want: []uint32{1, 2},
		},
		{
			name: "exact boundary strt",
			args: args{start: 1548576, end: BlockSize * 2, partSize: BlockSize},
			want: []uint32{1, 2},
		},
		{
			name: "exact boundary end",
			args: args{start: 0, end: 99, partSize: BlockSize},
			want: []uint32{0},
		},
		{
			name: "large range",
			args: args{start: 0, end: BlockSize*4 - 1, partSize: BlockSize},
			want: []uint32{0, 1, 2, 3},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ioindexes.Build(tt.args.start, tt.args.end, tt.args.partSize); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("Build() = %v, want %v", got, tt.want)
			}
		})
	}
}
