package cli

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCheckHostNested(t *testing.T) {
	for name, tc := range map[string]struct {
		params map[string]string // module -> parameters/nested content
		ok     bool
	}{
		"intel on":   {map[string]string{"kvm_intel": "Y\n"}, true},
		"intel off":  {map[string]string{"kvm_intel": "N\n"}, false},
		"amd on":     {map[string]string{"kvm_amd": "1\n"}, true},
		"amd off":    {map[string]string{"kvm_amd": "0\n"}, false},
		"not loaded": {nil, false},
	} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			for mod, v := range tc.params {
				dir := filepath.Join(root, mod, "parameters")
				if err := os.MkdirAll(dir, 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, "nested"), []byte(v), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if err := checkHostNested(root); (err == nil) != tc.ok {
				t.Errorf("checkHostNested = %v, want ok=%v", err, tc.ok)
			}
		})
	}
}
