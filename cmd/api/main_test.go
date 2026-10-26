package main

import "testing"

func TestConfiguration(t *testing.T) {
	for _, test := range []struct {
		name, address, path, wantAddress, wantPath string
		wantError                                  bool
	}{
		{"defaults", "", "", "127.0.0.1:8080", "data/app.db", false},
		{"custom", ":9090", "/tmp/items.db", ":9090", "/tmp/items.db", false},
		{"IPv6", "[::1]:8080", "", "[::1]:8080", "data/app.db", false},
		{"missing port", "localhost", "", "", "", true},
		{"invalid port", ":abc", "", "", "", true},
		{"zero port", ":0", "", "", "", true},
		{"overflow port", ":65536", "", "", "", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("HTTP_ADDR", test.address)
			t.Setenv("DB_PATH", test.path)
			address, path, err := configuration()
			if (err != nil) != test.wantError || address != test.wantAddress || path != test.wantPath {
				t.Fatalf("got (%q, %q, %v), want (%q, %q, error=%v)", address, path, err, test.wantAddress, test.wantPath, test.wantError)
			}
		})
	}
}
