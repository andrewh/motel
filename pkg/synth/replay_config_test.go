package synth

import (
	"strings"
	"testing"
)

func TestValidateReplayConfig(t *testing.T) {
	tests := []struct {
		name    string
		yaml    string
		wantErr string
	}{
		{
			name: "valid replay needs only mode and recording",
			yaml: "version: 1\nmode: replay\nrecording: rec.jsonl\n",
		},
		{
			name:    "replay without recording is rejected",
			yaml:    "version: 1\nmode: replay\n",
			wantErr: "requires a 'recording:'",
		},
		{
			name:    "unknown mode is rejected",
			yaml:    "version: 1\nmode: bogus\nrecording: rec.jsonl\n",
			wantErr: "unknown mode",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := ParseConfig([]byte(tt.yaml))
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			err = ValidateConfig(cfg)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if cfg.Mode != ModeReplay || cfg.Recording != "rec.jsonl" {
					t.Fatalf("mode/recording not parsed: %+v", cfg)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("got error %v, want containing %q", err, tt.wantErr)
			}
		})
	}
}
