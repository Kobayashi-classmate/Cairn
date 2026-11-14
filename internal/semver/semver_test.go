// SPDX-License-Identifier: AGPL-3.0-only

package semver

import "testing"

func TestParse(t *testing.T) {
	for _, ok := range []string{"0.1.0", "v1.2.3", "1.0.0-beta.1", "1.0.0-rc.1+build.5"} {
		if _, err := Parse(ok); err != nil {
			t.Errorf("%s: %v", ok, err)
		}
	}
	// Development builds and malformed versions do not parse.
	for _, bad := range []string{"8772440-dirty", "0.0.3~dev", "1.2", "01.2.3", "1.2.3-", "1.2.3-be$ta", "0.0.0-dev+x y"} {
		if _, err := Parse(bad); err == nil {
			t.Errorf("%s parsed", bad)
		}
	}
}

func TestCompareFollowsSemVerPrecedence(t *testing.T) {
	// SemVer 2.0.0 section 11 example ordering.
	order := []string{"1.0.0-alpha", "1.0.0-alpha.1", "1.0.0-alpha.beta", "1.0.0-beta", "1.0.0-beta.2", "1.0.0-beta.11", "1.0.0-rc.1", "1.0.0", "1.0.1", "1.10.0", "2.0.0"}
	for i := 0; i+1 < len(order); i++ {
		a, _ := Parse(order[i])
		b, _ := Parse(order[i+1])
		if Compare(a, b) != -1 || Compare(b, a) != 1 {
			t.Errorf("%s should be < %s", order[i], order[i+1])
		}
	}
}

func TestSatisfies(t *testing.T) {
	v, _ := Parse("0.3.1")
	for c, want := range map[string]bool{
		"": true, "*": true, ">=0.3.0": true, ">=0.3.0 <0.4.0": true, "<0.3.1": false,
		"=0.3.1": true, "0.3.1": true, ">0.3.1": false, "<=0.3.1": true,
	} {
		got, err := Satisfies(v, c)
		if err != nil || got != want {
			t.Errorf("%q: got %v %v want %v", c, got, err, want)
		}
	}
	if _, err := Satisfies(v, "~>0.3"); err == nil {
		t.Error("unsupported operator accepted")
	}
}
