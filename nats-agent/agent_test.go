package main

import "testing"

func TestIsNonRetryableStatus(t *testing.T) {
	tests := []struct {
		status int
		want   bool
	}{
		{status: 200, want: false},
		{status: 302, want: false},
		{status: 400, want: true},
		{status: 404, want: true},
		{status: 500, want: false},
	}

	for _, tt := range tests {
		got := isNonRetryableStatus(tt.status)
		if got != tt.want {
			t.Fatalf("status %d: got %v, want %v", tt.status, got, tt.want)
		}
	}
}

func TestIsRetryExhausted(t *testing.T) {
	if isRetryExhausted(1, 0) {
		t.Fatal("maxDeliver=0 should never be exhausted")
	}
	if isRetryExhausted(2, -1) {
		t.Fatal("maxDeliver<0 should never be exhausted")
	}
	if isRetryExhausted(2, 3) {
		t.Fatal("2/3 should not be exhausted")
	}
	if !isRetryExhausted(3, 3) {
		t.Fatal("3/3 should be exhausted")
	}
	if !isRetryExhausted(5, 3) {
		t.Fatal("5/3 should be exhausted")
	}
}

func TestBuildFailureReason(t *testing.T) {
	got := buildFailureReason(503, "", nil)
	if got != "http_status_503" {
		t.Fatalf("unexpected reason: %q", got)
	}

	got = buildFailureReason(400, "bad request", nil)
	if got != "http_status_400: bad request" {
		t.Fatalf("unexpected reason: %q", got)
	}
}
