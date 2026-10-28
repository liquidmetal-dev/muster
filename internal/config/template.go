package config

import (
	"encoding/json/jsontext"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"

	"github.com/liquidmetal-dev/flintlock/api/types"
	"google.golang.org/protobuf/encoding/protojson"
)

// decodeTemplate decodes a pool's microvm_template as a strict protojson
// MicroVMSpec. Fields that muster or battery own are rejected, and the
// kernel and root volume are required. Errors carry no path; the caller
// prefixes the pool's microvm_template path.
// managedFields are owned by muster or battery, keyed by their JSON name
// in both the protojson and the original proto form.
var managedFields = map[string]string{
	"id": "id", "namespace": "namespace", "uid": "uid",
	"createdAt": "created_at", "created_at": "created_at",
	"updatedAt": "updated_at", "updated_at": "updated_at",
	"deletedAt": "deleted_at", "deleted_at": "deleted_at",
}

// rejectManagedFields reports a managed key that is present in raw, even
// when its value is empty or null.
func rejectManagedFields(raw jsontext.Value) error {
	var keys map[string]jsontext.Value
	if err := jsonv2.Unmarshal(raw, &keys); err != nil {
		return fmt.Errorf("invalid MicroVMSpec: %w", err)
	}
	for key, name := range managedFields {
		if _, present := keys[key]; present {
			return fmt.Errorf("%s: managed by muster; remove it", name)
		}
	}
	return nil
}

func decodeTemplate(raw jsontext.Value) (*types.MicroVMSpec, error) {
	if len(raw) == 0 {
		return nil, errors.New("required")
	}
	var spec types.MicroVMSpec
	if err := (protojson.UnmarshalOptions{DiscardUnknown: false}).Unmarshal(raw, &spec); err != nil {
		return nil, fmt.Errorf("invalid MicroVMSpec: %w", err)
	}
	if err := rejectManagedFields(raw); err != nil {
		return nil, err
	}
	if spec.Kernel == nil {
		return nil, errors.New("kernel: required")
	}
	if spec.RootVolume == nil {
		return nil, errors.New("root_volume: required")
	}
	return &spec, nil
}
