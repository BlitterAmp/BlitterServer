package main

import (
	"testing"
)

func TestStripConfigArgs(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want []string
	}{
		{
			name: "bare --config with value",
			args: []string{"--config", "path.yaml", "--listen", "localhost"},
			want: []string{"--listen", "localhost"},
		},
		{
			name: "bare -config with value",
			args: []string{"-config", "path.yaml", "--listen", "localhost"},
			want: []string{"--listen", "localhost"},
		},
		{
			name: "--config=value form",
			args: []string{"--config=path.yaml", "--listen", "localhost"},
			want: []string{"--listen", "localhost"},
		},
		{
			name: "-config=value form",
			args: []string{"-config=path.yaml", "--listen", "localhost"},
			want: []string{"--listen", "localhost"},
		},
		{
			name: "mixed flags survive",
			args: []string{"--listen", "localhost", "--config=path.yaml", "--data-dir", "/tmp"},
			want: []string{"--listen", "localhost", "--data-dir", "/tmp"},
		},
		{
			name: "config flag at end without value",
			args: []string{"--listen", "localhost", "--config"},
			want: []string{"--listen", "localhost"},
		},
		{
			name: "empty args",
			args: []string{},
			want: []string{},
		},
		{
			name: "no config flag",
			args: []string{"--listen", "localhost", "--data-dir", "/tmp"},
			want: []string{"--listen", "localhost", "--data-dir", "/tmp"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := stripConfigArgs(tt.args)
			if len(got) != len(tt.want) {
				t.Errorf("stripConfigArgs(%v) returned %d args, want %d", tt.args, len(got), len(tt.want))
				t.Errorf("  got:  %v", got)
				t.Errorf("  want: %v", tt.want)
				return
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("stripConfigArgs(%v)[%d] = %q, want %q", tt.args, i, got[i], tt.want[i])
				}
			}
		})
	}
}
