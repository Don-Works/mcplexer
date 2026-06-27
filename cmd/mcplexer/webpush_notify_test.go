package main

import "testing"

func TestWebPushSubscriberSelection(t *testing.T) {
	tests := []struct {
		name      string
		explicit  string
		publicURL string
		want      string
	}{
		{
			name:      "explicit mailto wins",
			explicit:  "mailto:ops@example.com",
			publicURL: "https://app.example.com",
			want:      "mailto:ops@example.com",
		},
		{
			name:      "explicit https wins",
			explicit:  "https://notify.example.com",
			publicURL: "https://app.example.com",
			want:      "https://notify.example.com",
		},
		{
			name:      "public url fallback",
			publicURL: "https://app.example.com",
			want:      "https://app.example.com",
		},
		{
			name:      "bare public host becomes https",
			publicURL: "app.example.com",
			want:      "https://app.example.com",
		},
		{
			name:      "invalid explicit falls back to public url",
			explicit:  "http://insecure.example.com",
			publicURL: "https://app.example.com",
			want:      "https://app.example.com",
		},
		{
			name: "default is valid https uri",
			want: defaultWebPushSubscriber,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := webPushSubscriber(tt.explicit, tt.publicURL); got != tt.want {
				t.Fatalf("webPushSubscriber(%q, %q) = %q, want %q", tt.explicit, tt.publicURL, got, tt.want)
			}
		})
	}
}

func TestLoadConfigReadsWebPushSubject(t *testing.T) {
	t.Setenv("MCPLEXER_WEB_PUSH_SUBJECT", "mailto:ops@example.com")
	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if cfg.WebPushSubject != "mailto:ops@example.com" {
		t.Fatalf("WebPushSubject = %q, want %q", cfg.WebPushSubject, "mailto:ops@example.com")
	}
}
