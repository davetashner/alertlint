package main

import (
	"strings"
	"testing"
)

func TestRun(t *testing.T) {
	tests := []struct {
		name       string
		args       []string
		wantCode   int
		wantOut    string
		wantErrOut string
	}{
		{name: "no args shows usage", args: nil, wantCode: 2, wantErrOut: "Usage:"},
		{name: "version", args: []string{"version"}, wantCode: 0, wantOut: version},
		{name: "help", args: []string{"help"}, wantCode: 0, wantOut: "Usage:"},
		{name: "analyze requires tenant", args: []string{"analyze"}, wantCode: 2, wantErrOut: "--tenant is required"},
		{name: "worklist stub", args: []string{"worklist"}, wantCode: 1, wantErrOut: "not implemented"},
		{name: "identity stub", args: []string{"identity"}, wantCode: 1, wantErrOut: "not implemented"},
		{name: "unknown command", args: []string{"bogus"}, wantCode: 2, wantErrOut: `unknown command "bogus"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr strings.Builder
			code := run(tt.args, &stdout, &stderr)
			if code != tt.wantCode {
				t.Errorf("run(%v) code = %d, want %d", tt.args, code, tt.wantCode)
			}
			if tt.wantOut != "" && !strings.Contains(stdout.String(), tt.wantOut) {
				t.Errorf("stdout = %q, want it to contain %q", stdout.String(), tt.wantOut)
			}
			if tt.wantErrOut != "" && !strings.Contains(stderr.String(), tt.wantErrOut) {
				t.Errorf("stderr = %q, want it to contain %q", stderr.String(), tt.wantErrOut)
			}
		})
	}
}
