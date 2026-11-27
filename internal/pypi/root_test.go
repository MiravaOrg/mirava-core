package pypi

import (
	"testing"
)

func TestSimpleIndexURL(t *testing.T) {
	cases := []struct{ in, want string }{
		{"https://pypi.tuna.tsinghua.edu.cn/simple/", "https://pypi.tuna.tsinghua.edu.cn/simple/"},
		{"https://pypi.tuna.tsinghua.edu.cn/simple", "https://pypi.tuna.tsinghua.edu.cn/simple/"},
		{"https://mirror.example.org/pypi/web/simple/", "https://mirror.example.org/pypi/web/simple/"},
		{"https://mirror.example.org/pypi", "https://mirror.example.org/pypi/simple/"},
		{"https://mirror.example.org/", "https://mirror.example.org/simple/"},
	}
	for _, c := range cases {
		got, err := simpleIndexURL(c.in)
		if err != nil {
			t.Fatalf("%s: %v", c.in, err)
		}
		if got != c.want {
			t.Errorf("simpleIndexURL(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
