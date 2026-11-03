package github

import "testing"

func TestParseBaseURL(t *testing.T) {
	tests := map[string]string{
		"":                               "https://api.github.com/",
		"https://api.github.com":         "https://api.github.com/",
		"https://api.github.com/":        "https://api.github.com/",
		"https://ghe.example.com":        "https://ghe.example.com/api/v3/",
		"https://ghe.example.com/":       "https://ghe.example.com/api/v3/",
		"https://ghe.example.com/api/v3": "https://ghe.example.com/api/v3/",
	}
	for in, want := range tests {
		c, err := New(Config{Token: "x", BaseURL: in})
		if err != nil {
			t.Fatalf("New(%q): %v", in, err)
		}
		if got := c.gh.BaseURL(); got != want {
			t.Errorf("BaseURL(%q) = %q, want %q", in, got, want)
		}
	}
}
