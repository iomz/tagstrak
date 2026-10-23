package main

import "testing"

// TestParseUintRejectsSkew checks that a decimalMaximum the generator cannot
// represent fails generation instead of producing a field that accepts only
// zero.
func TestParseUintRejectsSkew(t *testing.T) {
	for _, s := range []string{"", "abc", "-1", "18446744073709551616"} {
		if _, err := parseUint("serial", s); err == nil {
			t.Errorf("parseUint(%q) succeeded, want an error", s)
		}
	}
	if v, err := parseUint("filter", "7"); err != nil || v != 7 {
		t.Errorf("parseUint(\"7\") = %d, %v; want 7, nil", v, err)
	}
}
