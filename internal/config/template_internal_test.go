package config

import (
	"strings"
	"testing"
)

const (
	tmplKernel = `"kernel":{"image":"k"}`
	tmplRoot   = `"rootVolume":{"id":"root","source":{"containerSource":"img"}}`
)

func TestDecodeTemplate(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string // "" means decode must succeed
	}{
		{"camelCase", `{"vcpu":2,"memoryInMb":4096,` + tmplKernel + `,` + tmplRoot + `}`, ""},
		{"snake_case", `{"vcpu":2,"memory_in_mb":4096,"kernel":{"image":"k"},"root_volume":{"id":"root","source":{"container_source":"img"}}}`, ""},
		{"labels and metadata ok", `{` + tmplKernel + `,` + tmplRoot + `,"labels":{"a":"b"},"metadata":{"k":"dg=="}}`, ""},
		{"unknown field", `{"vcpus":2,` + tmplKernel + `,` + tmplRoot + `}`, `unknown field "vcpus"`},
		{"id managed", `{"id":"x",` + tmplKernel + `,` + tmplRoot + `}`, "id: managed by muster"},
		{"id empty string", `{"id":"",` + tmplKernel + `,` + tmplRoot + `}`, "id: managed by muster"},
		{"uid null", `{"uid":null,` + tmplKernel + `,` + tmplRoot + `}`, "uid: managed by muster"},
		{"created_at null", `{"created_at":null,` + tmplKernel + `,` + tmplRoot + `}`, "created_at: managed by muster"},
		{"namespace managed", `{"namespace":"x",` + tmplKernel + `,` + tmplRoot + `}`, "namespace: managed by muster"},
		{"uid managed", `{"uid":"x",` + tmplKernel + `,` + tmplRoot + `}`, "uid: managed by muster"},
		{"createdAt managed", `{"createdAt":"2026-01-01T00:00:00Z",` + tmplKernel + `,` + tmplRoot + `}`, "created_at: managed by muster"},
		{"updated_at managed", `{"updated_at":"2026-01-01T00:00:00Z",` + tmplKernel + `,` + tmplRoot + `}`, "updated_at: managed by muster"},
		{"deletedAt managed", `{"deletedAt":"2026-01-01T00:00:00Z",` + tmplKernel + `,` + tmplRoot + `}`, "deleted_at: managed by muster"},
		{"no kernel", `{` + tmplRoot + `}`, "kernel: required"},
		{"no root volume", `{` + tmplKernel + `}`, "root_volume: required"},
		{"not an object", `"a string"`, "invalid MicroVMSpec"},
		{"empty", ``, "required"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spec, err := decodeTemplate([]byte(tt.in))
			if tt.want != "" {
				if err == nil || !strings.Contains(err.Error(), tt.want) {
					t.Fatalf("error = %v, want one containing %q", err, tt.want)
				}
				return
			}
			if err != nil {
				t.Fatalf("error = %v", err)
			}
			if spec.Kernel == nil || spec.Kernel.Image != "k" {
				t.Fatalf("Kernel = %+v, want image k", spec.Kernel)
			}
			if strings.Contains(tt.name, "ase") && spec.MemoryInMb != 4096 {
				t.Fatalf("MemoryInMb = %d, want 4096", spec.MemoryInMb)
			}
		})
	}
}
