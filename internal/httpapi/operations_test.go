package httpapi

import "testing"

func TestTenderedJPYIsCanonicalAndExact(t *testing.T) {
	for _, input := range []string{"", "-1", "+1", "1.0", "1e3", " 1", "01", "１２", "9223372036854775808"} {
		if _, err := parseJPY(input); err == nil {
			t.Fatalf("accepted %q", input)
		}
	}
	for input, expected := range map[string]int64{"0": 0, "1100": 1100, "9007199254740993": 9007199254740993, "9223372036854775807": 9223372036854775807} {
		got, err := parseJPY(input)
		if err != nil || got != expected {
			t.Fatalf("%s => %d, %v", input, got, err)
		}
	}
}
