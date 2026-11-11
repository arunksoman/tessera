package version

import (
	"strings"
	"testing"
)

func TestCompare(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"1.2.3", "1.2.3", 0},
		{"v1.2.3", "1.2.3", 0},
		{"1.2.3", "1.10.0", -1},
		{"2.0.0", "1.99.99", 1},
		{"1.0.0-rc.1", "1.0.0", -1},
		{"1.0.0-rc.2", "1.0.0-rc.10", -1},
		{"1.0.0-alpha", "1.0.0-alpha.1", -1},
		{"1.0.0-alpha.beta", "1.0.0-alpha.1", 1},
		{"1.0.0+build.5", "1.0.0", 0},
		{"dev", "99.0.0", 1},
		{"0.1.0", "dev", -1},
		{"dev", "dev", 0},
	}
	for _, c := range cases {
		got, err := Compare(c.a, c.b)
		if err != nil || got != c.want {
			t.Errorf("Compare(%q, %q) = %d, %v; want %d", c.a, c.b, got, err, c.want)
		}
	}
	for _, bad := range []string{"1.2", "1.2.x", "01.2.3", "1.-2.3", "", "latest"} {
		if _, err := Compare(bad, "1.0.0"); err == nil {
			t.Errorf("%q parsed", bad)
		}
	}
}

func TestSupports(t *testing.T) {
	cases := []struct {
		v, min, max string
		ok          bool
		msg         string
	}{
		{"0.4.0", "", "", true, ""},
		{"0.4.0", "0.4.0", "", true, ""},
		{"0.3.9", "0.4.0", "", false, "needs tess 0.4.0 or newer"},
		{"0.4.0", "0.2.0", "0.4.0", true, ""},
		{"0.5.0", "", "0.4.0", false, "only up to tess 0.4.0"},
		{"dev", "9.0.0", "", true, ""},
		{"dev", "", "0.4.0", false, "only up to"},
		{"0.4.0", "four", "", false, "not a version"},
		{"0.4.0", "", "x", false, "not a version"},
	}
	for _, c := range cases {
		err := Supports(c.v, c.min, c.max)
		if (err == nil) != c.ok || (err != nil && !strings.Contains(err.Error(), c.msg)) {
			t.Errorf("Supports(%q, %q, %q) = %v", c.v, c.min, c.max, err)
		}
	}
}

func TestDescribe(t *testing.T) {
	if Describe("1.2.3") != "1.2.3" {
		t.Fatal("a release version changed")
	}
	if d := Describe(Dev); !strings.HasPrefix(d, Dev) {
		t.Fatalf("Describe(dev) = %q", d)
	}
}
