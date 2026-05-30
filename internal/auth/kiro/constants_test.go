package kiro

import "testing"

// TestIDCBrowserURL verifies the IAM Identity Center browser URL is the literal start URL
// with region + auth_method appended, and that an empty region falls back to DefaultRegion.
func TestIDCBrowserURL(t *testing.T) {
	cases := []struct {
		name     string
		startURL string
		region   string
		want     string
	}{
		{
			name:     "explicit region",
			startURL: "https://d-90660ceab3.awsapps.com/start",
			region:   "us-east-1",
			want:     "https://d-90660ceab3.awsapps.com/start&region=us-east-1&auth_method=idc",
		},
		{
			name:     "empty region defaults",
			startURL: "https://d-90660ceab3.awsapps.com/start",
			region:   "",
			want:     "https://d-90660ceab3.awsapps.com/start&region=" + DefaultRegion + "&auth_method=idc",
		},
		{
			name:     "whitespace trimmed",
			startURL: "  https://example.awsapps.com/start  ",
			region:   "  eu-west-1  ",
			want:     "https://example.awsapps.com/start&region=eu-west-1&auth_method=idc",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IDCBrowserURL(tc.startURL, tc.region); got != tc.want {
				t.Fatalf("IDCBrowserURL(%q, %q) = %q, want %q", tc.startURL, tc.region, got, tc.want)
			}
		})
	}
}
