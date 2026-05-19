package config

import (
	"reflect"
	"testing"
)

func TestFlattenCSV(t *testing.T) {
	tests := []struct {
		name string
		in   []string
		want []string
	}{
		{"empty", nil, []string{}},
		{"single plain", []string{"a@x.com"}, []string{"a@x.com"}},
		{"single csv", []string{"a@x.com,b@x.com"}, []string{"a@x.com", "b@x.com"}},
		{"single csv with spaces", []string{"a@x.com, b@x.com , c@x.com"}, []string{"a@x.com", "b@x.com", "c@x.com"}},
		{"multiple yaml entries", []string{"a@x.com", "b@x.com"}, []string{"a@x.com", "b@x.com"}},
		{"mix of csv + plain entries", []string{"a@x.com,b@x.com", "c@x.com"}, []string{"a@x.com", "b@x.com", "c@x.com"}},
		{"empty / trailing commas dropped", []string{"a@x.com,,b@x.com,"}, []string{"a@x.com", "b@x.com"}},
		{"only whitespace dropped", []string{"   ", ""}, []string{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := flattenCSV(tt.in)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("flattenCSV(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}
