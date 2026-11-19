package main

import "testing"

func TestBanner(t *testing.T) {
	if got, want := banner("mcloud-dummy", "1.2.3"), "\u2501\u2501 mcloud-dummy 1.2.3"; got != want {
		t.Errorf("banner without facts = %q, want %q", got, want)
	}
	if got, want := banner("mcloud-dummy", "1.2.3", "a", "b c"), "\u2501\u2501 mcloud-dummy 1.2.3 \u00b7 a \u00b7 b c"; got != want {
		t.Errorf("banner with facts = %q, want %q", got, want)
	}
}
