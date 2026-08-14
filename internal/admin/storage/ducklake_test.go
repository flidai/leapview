package storage

import "testing"

func TestFormatDuckLakeUUID(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		value []byte
		want  string
	}{
		{
			name:  "binary UUID",
			value: []byte{0x01, 0xb5, 0xee, 0x02, 0xaa, 0x5d, 0x73, 0x53, 0x99, 0x20, 0x1e, 0xa7, 0x1f, 0x67, 0x88, 0xfe},
			want:  "01b5ee02-aa5d-7353-9920-1ea71f6788fe",
		},
		{
			name:  "text UUID",
			value: []byte("01B5EE02-AA5D-7353-9920-1EA71F6788FE"),
			want:  "01b5ee02-aa5d-7353-9920-1ea71f6788fe",
		},
		{
			name:  "compact text UUID",
			value: []byte("01b5ee02aa5d735399201ea71f6788fe"),
			want:  "01b5ee02-aa5d-7353-9920-1ea71f6788fe",
		},
		{
			name:  "unexpected binary value",
			value: []byte{0xff, 0x00},
			want:  "ff00",
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := formatDuckLakeUUID(test.value); got != test.want {
				t.Fatalf("formatDuckLakeUUID() = %q, want %q", got, test.want)
			}
		})
	}
}
