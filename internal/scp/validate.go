package scp

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/shaansundar/doppio/schema"
)

var (
	compileOnce sync.Once
	manifestSch *jsonschema.Schema
	sceneSch    *jsonschema.Schema
	compileErr  error
)

func compileSchemas() {
	c := jsonschema.NewCompiler()
	for _, name := range []string{"manifest", "scene"} {
		path := "scp/v1/" + name + ".schema.json"
		data, err := schema.FS.ReadFile(path)
		if err != nil {
			compileErr = fmt.Errorf("failed to read embedded schema %s: %w", path, err)
			return
		}
		doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(data))
		if err != nil {
			compileErr = fmt.Errorf("failed to parse schema %s: %w", path, err)
			return
		}
		if err := c.AddResource(path, doc); err != nil {
			compileErr = fmt.Errorf("failed to add schema %s: %w", path, err)
			return
		}
	}
	if manifestSch, compileErr = c.Compile("scp/v1/manifest.schema.json"); compileErr != nil {
		return
	}
	sceneSch, compileErr = c.Compile("scp/v1/scene.schema.json")
}

// ValidateManifest checks v (a Manifest or decoded JSON) against the v1 schema.
func ValidateManifest(v any) error {
	return validate(v, func() *jsonschema.Schema { return manifestSch })
}

// ValidateScene checks v (a Scene or decoded JSON) against the v1 schema.
func ValidateScene(v any) error { return validate(v, func() *jsonschema.Schema { return sceneSch }) }

func validate(v any, pick func() *jsonschema.Schema) error {
	compileOnce.Do(compileSchemas)
	if compileErr != nil {
		return compileErr
	}
	data, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("failed to encode for validation: %w", err)
	}
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("failed to decode for validation: %w", err)
	}
	if err := pick().Validate(doc); err != nil {
		return fmt.Errorf("schema validation failed: %w", err)
	}
	return nil
}
