package main

import "testing"

func TestFit(t *testing.T) {
	w12 := textWidth(face(regularFont, 12), "qinheng-display")
	cases := []struct {
		name string
		maxW int
		want string
	}{
		{"fits at the largest size", 500, "qinheng-display"},
		{"fits only at the smallest size", w12, "qinheng-display"},
		{"too wide even at the smallest size", w12 - 1, "qinheng-displ…"},
	}
	for _, c := range cases {
		if _, got := fit(regularFont, "qinheng-display", 17, 12, c.maxW); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}
