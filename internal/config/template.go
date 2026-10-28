package config

import (
	"encoding/json/jsontext"
	"errors"
	"fmt"

	"github.com/liquidmetal-dev/flintlock/api/types"
	"google.golang.org/protobuf/encoding/protojson"
)

// decodeTemplate decodes a pool's microvm_template as a strict protojson
// MicroVMSpec. Fields that muster or battery own are rejected, and the
// kernel and root volume are required. Errors carry no path; the caller
// prefixes the pool's microvm_template path.
func decodeTemplate(raw jsontext.Value) (*types.MicroVMSpec, error) {
	if len(raw) == 0 {
		return nil, errors.New("required")
	}
	var spec types.MicroVMSpec
	if err := (protojson.UnmarshalOptions{DiscardUnknown: false}).Unmarshal(raw, &spec); err != nil {
		return nil, fmt.Errorf("invalid MicroVMSpec: %w", err)
	}
	managed := []struct {
		name string
		set  bool
	}{
		{"id", spec.Id != ""},
		{"namespace", spec.Namespace != ""},
		{"uid", spec.Uid != nil},
		{"created_at", spec.CreatedAt != nil},
		{"updated_at", spec.UpdatedAt != nil},
		{"deleted_at", spec.DeletedAt != nil},
	}
	for _, m := range managed {
		if m.set {
			return nil, fmt.Errorf("%s: managed by muster; remove it", m.name)
		}
	}
	if spec.Kernel == nil {
		return nil, errors.New("kernel: required")
	}
	if spec.RootVolume == nil {
		return nil, errors.New("root_volume: required")
	}
	return &spec, nil
}
