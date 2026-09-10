package rowformat_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/tranceh2/shep/internal/preview"
	"github.com/tranceh2/shep/internal/rowformat"
)

func TestRender(t *testing.T) {
	data := rowformat.Context{
		Path:        "/workspace/shep",
		Label:       "shep",
		Icon:        "󰊢",
		TabNumber:   "3",
		AgentStatus: "working",
	}

	fieldCases := []struct {
		name     string
		format   string
		want     string
		data     rowformat.Context
		wantErr  bool
		errMatch string
	}{
		{
			name:   "literal text",
			format: "workspace",
			want:   "workspace",
			data:   data,
		},
		{
			name:   "empty format",
			format: "",
			want:   "",
			data:   data,
		},
		{
			name:   "path field",
			format: "{{.Path}}",
			want:   "/workspace/shep",
			data:   data,
		},
		{
			name:   "label field",
			format: "{{.Label}}",
			want:   "shep",
			data:   data,
		},
		{
			name:   "icon field",
			format: "{{.Icon}}",
			want:   "󰊢",
			data:   data,
		},
		{
			name:   "tab number field",
			format: "{{.TabNumber}}",
			want:   "3",
			data:   data,
		},
		{
			name:   "agent status field",
			format: "{{.AgentStatus}}",
			want:   "working",
			data:   data,
		},
		{
			name:   "metadata index field",
			format: `{{ index .Meta "context" }}`,
			want:   "cluster prod west",
			data: rowformat.Context{
				Meta: map[string]string{"context": "cluster prod west"},
			},
		},
		{
			name:   "conditional label present",
			format: "{{if .Label}}{{.Label}} · {{end}}{{.Path}}",
			want:   "shep · /workspace/shep",
			data:   data,
		},
		{
			name:   "conditional label collapses when empty",
			format: "{{if .Label}}{{.Label}} · {{end}}{{.Path}}",
			want:   "/workspace/shep",
			data: rowformat.Context{
				Path: "/workspace/shep",
			},
		},
		{
			name:     "malformed template",
			format:   "{{if .Path}}",
			data:     data,
			wantErr:  true,
			errMatch: "template",
		},
		{
			name:     "unknown field fails during execution",
			format:   "{{.Unknown}}",
			data:     data,
			wantErr:  true,
			errMatch: "Unknown",
		},
	}

	for _, tt := range fieldCases {
		t.Run(tt.name, func(t *testing.T) {
			got, err := rowformat.Render(tt.format, tt.data)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("Render(%q, %#v) error = nil, want error", tt.format, tt.data)
				}
				if !strings.Contains(err.Error(), tt.errMatch) {
					t.Fatalf("Render(%q, %#v) error = %q, want it to contain %q", tt.format, tt.data, err, tt.errMatch)
				}
				return
			}
			if err != nil {
				t.Fatalf("Render(%q, %#v) error = %v", tt.format, tt.data, err)
			}
			if got != tt.want {
				t.Errorf("Render(%q, %#v) = %q, want %q", tt.format, tt.data, got, tt.want)
			}
		})
	}
}

func TestTokenize(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    []string
		wantErr string
	}{
		{
			name:  "splits whitespace",
			input: "open\t/path\n--background",
			want:  []string{"open", "/path", "--background"},
		},
		{
			name:  "keeps double quoted run together",
			input: `open "/Users/me/My Project"`,
			want:  []string{"open", "/Users/me/My Project"},
		},
		{
			name:  "keeps single quoted run together",
			input: "printf 'hello world'",
			want:  []string{"printf", "hello world"},
		},
		{
			name:  "preserves empty quoted argument",
			input: `cmd "" ''`,
			want:  []string{"cmd", "", ""},
		},
		{
			name:    "rejects unterminated quote",
			input:   `open "/Users/me/My Project`,
			wantErr: "unterminated quote in preview command",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := rowformat.Tokenize(tt.input)
			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("Tokenize(%q) error = nil, want %q", tt.input, tt.wantErr)
				}
				if err.Error() != tt.wantErr {
					t.Errorf("Tokenize(%q) error = %q, want %q", tt.input, err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Tokenize(%q) error = %v", tt.input, err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("Tokenize(%q) = %#v, want %#v", tt.input, got, tt.want)
			}
		})
	}
}

func TestTokenizeMatchesPreviewParseCommand(t *testing.T) {
	inputs := []string{
		"open /tmp/project",
		`open "/Users/me/My Project"`,
		"printf 'hello world'",
		`cmd "" ''`,
		"open\t/path\n--background",
		`open "/Users/me/My Project`,
	}

	for _, input := range inputs {
		t.Run(input, func(t *testing.T) {
			got, gotErr := rowformat.Tokenize(input)
			want, wantErr := preview.ParseCommand(input, rowformat.Context{})

			if (gotErr != nil) != (wantErr != nil) {
				t.Fatalf("Tokenize(%q) error = %v, preview.ParseCommand error = %v", input, gotErr, wantErr)
			}
			if gotErr != nil {
				if gotErr.Error() != wantErr.Error() {
					t.Errorf("Tokenize(%q) error = %q, preview.ParseCommand error = %q", input, gotErr, wantErr)
				}
				return
			}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("Tokenize(%q) = %#v, preview.ParseCommand = %#v", input, got, want)
			}
		})
	}
}
