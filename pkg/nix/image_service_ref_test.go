package nix

import "testing"

func TestParseNixRef(t *testing.T) {
	const path = "/nix/store/0123456789abcdfghijklmnpqrsvwxyz-nix-image-redis.tar"
	const digest = "sha256:0d9b3f2e1c0a"
	for _, tc := range []struct {
		ref, path, digest string
	}{
		{"nix:0" + path, path, ""},
		{"nix:0" + path + ":latest", path, ""},
		{"nix:0" + path + ":v1.2.3", path, ""},
		{"nix:0" + path + "@" + digest, path, digest},
		{"nix:0" + path + ":v1.2.3@" + digest, path, digest},
	} {
		p, d := parseNixRef(tc.ref)
		if p != tc.path || d != tc.digest {
			t.Errorf("parseNixRef(%q) = %q, %q; want %q, %q", tc.ref, p, d, tc.path, tc.digest)
		}
	}
}
