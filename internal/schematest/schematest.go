package schematest

import (
	"bytes"
	"testing"

	hubd "github.com/kloudkit/hubd/protocol"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

const id = "https://hubd/protocol/client.schema.json"

type Schemas struct{ Client, Server *jsonschema.Schema }

func Load(t testing.TB) Schemas {
	t.Helper()
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(hubd.Schema))
	if err != nil {
		t.Fatal(err)
	}
	c := jsonschema.NewCompiler()
	c.AssertFormat()
	if err := c.AddResource(id, doc); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Compile(id); err != nil {
		t.Fatal(err)
	}
	return Schemas{Client: c.MustCompile(id + "#/$defs/clientMessage"), Server: c.MustCompile(id + "#/$defs/serverMessage")}
}

func Check(t testing.TB, s *jsonschema.Schema, frame []byte) {
	t.Helper()
	v, err := jsonschema.UnmarshalJSON(bytes.NewReader(frame))
	if err != nil {
		t.Errorf("not JSON: %s", frame)
		return
	}
	if err := s.Validate(v); err != nil {
		t.Errorf("frame %s fails the schema: %v", frame, err)
	}
}
