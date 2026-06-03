package main

import (
	"testing"
)

func TestWrapAddressHeader(t *testing.T) {
	tests := []struct {
		name   string
		prefix string
		value  string
		width  int
		want   []string
	}{
		{
			name:   "short line no wrap",
			prefix: "    To: ",
			value:  "alice@example.com",
			width:  80,
			want:   []string{"    To: alice@example.com"},
		},
		{
			name:   "wraps at comma boundary",
			prefix: "    To: ",
			value:  "alice@example.com, bob@example.com, charlie@example.com",
			width:  40,
			want: []string{
				"    To: alice@example.com,",
				"        bob@example.com,",
				"        charlie@example.com",
			},
		},
		{
			name:   "zero width returns single line",
			prefix: "    To: ",
			value:  "alice@example.com, bob@example.com",
			width:  0,
			want:   []string{"    To: alice@example.com, bob@example.com"},
		},
		{
			name:   "CC prefix",
			prefix: "    CC: ",
			value:  "one@x.com, two@x.com, three@x.com",
			width:  30,
			want: []string{
				"    CC: one@x.com, two@x.com,",
				"        three@x.com",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := wrapAddressHeader(tt.prefix, tt.value, tt.width)
			if len(got) != len(tt.want) {
				t.Fatalf("got %d lines, want %d:\n  got:  %q\n  want: %q", len(got), len(tt.want), got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("line %d: got %q, want %q", i, got[i], tt.want[i])
				}
			}
		})
	}
}
