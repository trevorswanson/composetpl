package chart

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

// noLoader refuses to fetch $ref'd schemas from files or the network.
type noLoader struct{}

func (noLoader) Load(url string) (any, error) {
	return nil, fmt.Errorf("loading %s: external $ref is not supported in values.schema.json", url)
}

// ValidateValues checks computed values against the chart's values.schema.json,
// as Helm does on install, upgrade, template and lint. A chart without a schema
// always passes.
func (c *Chart) ValidateValues(vals map[string]interface{}) error {
	if len(bytes.TrimSpace(c.Schema)) == 0 {
		return nil
	}
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(c.Schema))
	if err != nil {
		return fmt.Errorf("%s/values.schema.json: %w", c.Metadata.Name, err)
	}
	comp := jsonschema.NewCompiler()
	comp.UseLoader(noLoader{})
	const url = "file:///values.schema.json"
	if err := comp.AddResource(url, doc); err != nil {
		return err
	}
	sch, err := comp.Compile(url)
	if err != nil {
		return fmt.Errorf("%s/values.schema.json: %w", c.Metadata.Name, err)
	}

	// Round-trip through JSON so every number and map type is one the validator knows.
	raw, err := json.Marshal(vals)
	if err != nil {
		return err
	}
	inst, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		return err
	}
	if err := sch.Validate(inst); err != nil {
		var ve *jsonschema.ValidationError
		if errors.As(err, &ve) {
			return fmt.Errorf("values don't meet the specifications of the schema in chart %s:\n%s",
				c.Metadata.Name, indent(ve.Error()))
		}
		return err
	}
	return nil
}

func indent(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	for i, l := range lines {
		lines[i] = "  " + l
	}
	return strings.Join(lines, "\n")
}
