package main

import (
	"reflect"
	"testing"
)

func TestTextStrokesReplaceAndAppend(t *testing.T) {
	tests := []struct {
		name  string
		clear bool
		want  [][]byte
	}{
		{"append", false, [][]byte{{0xe1, 0x04}, {0x1f}, {0xe1, 0x38}}},
		{"replace", true, [][]byte{{0xe3, 0x04}, {0x2a}, {0xe1, 0x04}, {0x1f}, {0xe1, 0x38}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := textStrokes("A2?", tt.clear)
			if err != nil || !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("got %x, %v; want %x", got, err, tt.want)
			}
		})
	}
}
