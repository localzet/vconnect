package android

import "testing"

func TestIsCommandLine(t *testing.T) {
	tests := []struct {
		line string
		want bool
	}{
		{line: "JOIN:{}", want: true},
		{line: "AUTH:{}", want: true},
		{line: "SERVICE_EGRESS:auto", want: true},
		{line: "STOP", want: true},
		{line: "example.com", want: false},
	}

	for _, test := range tests {
		t.Run(test.line, func(t *testing.T) {
			if got := isCommandLine(test.line); got != test.want {
				t.Fatalf("isCommandLine(%q) = %t, want %t", test.line, got, test.want)
			}
		})
	}
}
