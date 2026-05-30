//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

package runstate

import (
	"bytes"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

//go:embed schemas/v1/*.json
var schemaFS embed.FS

const (
	schemaDir     = "schemas/v1"
	schemaBaseURL = "https://choria.io/schemas/io.choria.fisk-ai.v1"
)

// ErrNoSchema is returned when a record carries a protocol id that has no schema.
var ErrNoSchema = errors.New("no schema for protocol")

// protocolSchemaFile maps each record protocol id to the schema that validates
// it. common.json holds shared definitions and validates no record directly.
var protocolSchemaFile = map[Protocol]string{
	MetaProtocol:       "session.meta.json",
	AssistantProtocol:  "session.assistant.json",
	UserProtocol:       "session.user.json",
	ToolResultProtocol: "session.tool_result.json",
	TerminalProtocol:   "session.terminal.json",
}

// Validator validates record bodies against the embedded v1 JSON schemas. A
// stored record is self describing via its protocol id, so any consumer, reading
// a journal line or a JetStream message, can validate it without knowing where it
// came from.
type Validator struct {
	schemas map[Protocol]*jsonschema.Schema
}

// NewValidator compiles the embedded schemas. The compiled Validator is safe for
// concurrent use and is intended to be built once and reused.
func NewValidator() (*Validator, error) {
	compiler := jsonschema.NewCompiler()

	entries, err := fs.ReadDir(schemaFS, schemaDir)
	if err != nil {
		return nil, err
	}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		raw, err := schemaFS.ReadFile(schemaDir + "/" + entry.Name())
		if err != nil {
			return nil, err
		}

		doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
		if err != nil {
			return nil, fmt.Errorf("parsing schema %s: %w", entry.Name(), err)
		}

		obj, ok := doc.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("schema %s is not an object", entry.Name())
		}

		id, ok := obj["$id"].(string)
		if !ok {
			return nil, fmt.Errorf("schema %s has no $id", entry.Name())
		}

		err = compiler.AddResource(id, doc)
		if err != nil {
			return nil, fmt.Errorf("adding schema %s: %w", entry.Name(), err)
		}
	}

	v := &Validator{schemas: make(map[Protocol]*jsonschema.Schema, len(protocolSchemaFile))}

	for protocol, file := range protocolSchemaFile {
		sch, err := compiler.Compile(schemaBaseURL + "/" + file)
		if err != nil {
			return nil, fmt.Errorf("compiling schema %s: %w", file, err)
		}

		v.schemas[protocol] = sch
	}

	return v, nil
}

// Validate checks a raw record body against the schema for its protocol id. It
// returns ErrNoSchema when the protocol id has no schema.
func (v *Validator) Validate(data []byte) error {
	var probe struct {
		Protocol Protocol `json:"protocol"`
	}

	err := json.Unmarshal(data, &probe)
	if err != nil {
		return err
	}

	sch, ok := v.schemas[probe.Protocol]
	if !ok {
		return fmt.Errorf("%w: %q", ErrNoSchema, probe.Protocol)
	}

	inst, err := jsonschema.UnmarshalJSON(bytes.NewReader(data))
	if err != nil {
		return err
	}

	return sch.Validate(inst)
}

// ValidateRecord marshals a record and validates the result against the schema
// for its protocol id.
func (v *Validator) ValidateRecord(rec Record) error {
	data, err := json.Marshal(rec)
	if err != nil {
		return err
	}

	return v.Validate(data)
}
