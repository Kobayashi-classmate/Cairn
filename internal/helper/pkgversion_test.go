// SPDX-License-Identifier: AGPL-3.0-only

package helper

import "testing"

func TestDebVersionCompare(t *testing.T) {
	for _, tc := range []struct {
		a, b string
		want int
	}{
		{"1.26.3-3+deb13u10", "1.26.3-3+deb13u10", 0},
		{"1.26.3-3+deb13u10", "1.26.3-3+deb13u11", -1},
		{"1.26.3-3+deb13u9", "1.26.3-3+deb13u10", -1},
		{"1:11.8.3-0+deb13u1", "11.9.0-1", 1}, // the epoch first
		{"1.0~rc1-1", "1.0-1", -1},            // "~" before the release
		{"1.0-1", "1.0-1~bpo1", 1},
		{"1.0", "1.0-0", 0},
		{"1.0a", "1.0", 1},
		{"1.0+b1", "1.0", 1},
		{"8.4.27-1~deb13u1", "8.4.27-1", -1},
		{"2.10", "2.9", 1},
		{"1.010", "1.10", 0},
	} {
		got := debVersionCompare(tc.a, tc.b)
		if sign(got) != tc.want || sign(debVersionCompare(tc.b, tc.a)) != -tc.want {
			t.Errorf("%s vs %s: %d, want %d", tc.a, tc.b, got, tc.want)
		}
	}
}

func TestRPMVersionCompare(t *testing.T) {
	for _, tc := range []struct {
		a, b string
		want int
	}{
		{"1.26.3-1.el9", "1.26.3-1.el9", 0},
		{"2:1.26.3-1.el9", "2:1.26.4-1.el9", -1},
		{"1:1.20.1-1.el9", "1.26.3-1.el9", 1}, // the epoch first
		{"1.26.3-1.el9_6", "1.26.3-1.el9", 1},
		{"1.0~rc1-1", "1.0-1", -1},
		{"1.0^git1-1", "1.0-1", 1},
		{"1.0^git1-1", "1.0.1-1", -1},
		{"1.0a-1", "1.0-1", 1},
		{"1.10-1", "1.9-1", 1},
		{"1.010-1", "1.10-1", 0},
		{"8.3.19-1.module+el9.6.0", "8.3.19-1.module+el9.5.0", 1},
		{"1.a-1", "1.1-1", -1}, // numbers sort after letters
	} {
		got := rpmEVRCompare(tc.a, tc.b)
		if sign(got) != tc.want || sign(rpmEVRCompare(tc.b, tc.a)) != -tc.want {
			t.Errorf("%s vs %s: %d, want %d", tc.a, tc.b, got, tc.want)
		}
	}
}

func sign(n int) int {
	switch {
	case n < 0:
		return -1
	case n > 0:
		return 1
	}
	return 0
}
