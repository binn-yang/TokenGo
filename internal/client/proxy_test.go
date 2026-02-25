package client

import (
	"net/http"
	"testing"
)

func TestDetectStreaming(t *testing.T) {
	tests := []struct {
		name   string
		body   []byte
		url    string
		accept string
		want   bool
	}{
		{
			name: "JSON body stream=true",
			body: []byte(`{"model":"gpt-4","stream":true}`),
			url:  "/v1/chat/completions",
			want: true,
		},
		{
			name: "JSON body stream=false",
			body: []byte(`{"model":"gpt-4","stream":false}`),
			url:  "/v1/chat/completions",
			want: false,
		},
		{
			name: "JSON body no stream field",
			body: []byte(`{"model":"gpt-4"}`),
			url:  "/v1/chat/completions",
			want: false,
		},
		{
			name: "URL contains stream (Gemini)",
			body: nil,
			url:  "/v1beta/models/gemini:streamGenerateContent",
			want: true,
		},
		{
			name: "Accept header text/event-stream",
			body:   nil,
			url:    "/v1/chat/completions",
			accept: "text/event-stream",
			want:   true,
		},
		{
			name: "Accept header application/json",
			body:   nil,
			url:    "/v1/chat/completions",
			accept: "application/json",
			want:   false,
		},
		{
			name: "empty body and normal URL",
			body: nil,
			url:  "/v1/models",
			want: false,
		},
		{
			name: "invalid JSON body",
			body: []byte(`not json`),
			url:  "/v1/chat/completions",
			want: false,
		},
		{
			name: "stream field is string not bool",
			body: []byte(`{"stream":"true"}`),
			url:  "/v1/chat/completions",
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r, _ := http.NewRequest("POST", "http://localhost"+tt.url, nil)
			if tt.accept != "" {
				r.Header.Set("Accept", tt.accept)
			}
			got := detectStreaming(tt.body, r)
			if got != tt.want {
				t.Errorf("detectStreaming() = %v, want %v", got, tt.want)
			}
		})
	}
}
