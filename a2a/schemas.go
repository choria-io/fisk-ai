//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

package a2a

import (
	"bytes"
	"embed"
	"encoding/json"
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

// protocolSchemaFile maps each message protocol id to the schema that validates
// it. common.json holds shared definitions and validates no message directly.
var protocolSchemaFile = map[string]string{
	RequestProtocol:     "request.json",
	EventProtocol:       "event.json",
	ResultProtocol:      "result.json",
	ErrorProtocol:       "error.json",
	CancelProtocol:      "cancel.json",
	AckProtocol:         "ack.json",
	ToolRequestProtocol: "tool.request.json",
	ToolReplyProtocol:   "tool.reply.json",

	DiscoveryRequestProtocol: "discovery.request.json",
	DiscoveryReplyProtocol:   "discovery.reply.json",
}

// Validator validates message bodies against the embedded v1 JSON schemas.
type Validator struct {
	schemas map[string]*jsonschema.Schema
}

// NewValidator compiles the embedded schemas. The compiled Validator is
// safe for concurrent use and is intended to be built once and reused.
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
			return nil, fmt.Errorf("%w: schema %s is not an object", ErrInvalidMessage, entry.Name())
		}

		id, ok := obj["$id"].(string)
		if !ok {
			return nil, fmt.Errorf("%w: schema %s has no $id", ErrInvalidMessage, entry.Name())
		}

		err = compiler.AddResource(id, doc)
		if err != nil {
			return nil, fmt.Errorf("adding schema %s: %w", entry.Name(), err)
		}
	}

	v := &Validator{schemas: make(map[string]*jsonschema.Schema, len(protocolSchemaFile))}

	for protocol, file := range protocolSchemaFile {
		sch, err := compiler.Compile(schemaBaseURL + "/" + file)
		if err != nil {
			return nil, fmt.Errorf("compiling schema %s: %w", file, err)
		}

		v.schemas[protocol] = sch
	}

	return v, nil
}

// Validate checks a raw message body against the schema for its protocol id. It
// returns ErrUnknownProtocol when the protocol id has no schema.
func (v *Validator) Validate(data []byte) error {
	var probe struct {
		Protocol string `json:"protocol"`
	}

	err := json.Unmarshal(data, &probe)
	if err != nil {
		return err
	}

	sch, ok := v.schemas[probe.Protocol]
	if !ok {
		return fmt.Errorf("%w: %q", ErrUnknownProtocol, probe.Protocol)
	}

	inst, err := jsonschema.UnmarshalJSON(bytes.NewReader(data))
	if err != nil {
		return err
	}

	return sch.Validate(inst)
}

// ValidateMessage marshals a message and validates the result against the schema
// for its protocol id.
func (v *Validator) ValidateMessage(msg any) error {
	data, err := json.Marshal(msg)
	if err != nil {
		return err
	}

	return v.Validate(data)
}
