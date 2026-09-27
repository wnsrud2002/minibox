package cgroup

import "testing"

func TestParseSize(t *testing.T) {
	for in, want := range map[string]int64{"64m": 64 << 20, "1G": 1 << 30, "512k": 512 << 10, "1000": 1000} {
		if got, err := ParseSize(in); err != nil || got != want {
			t.Errorf("ParseSize(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
	for _, in := range []string{"", "m", "-1m", "abc"} {
		if _, err := ParseSize(in); err == nil {
			t.Errorf("ParseSize(%q) should fail", in)
		}
	}
}
