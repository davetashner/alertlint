package main

import (
	"bytes"
	_ "embed"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

// contract_schema.json is a build-time copy of
// schemas/output-contract-v1.json; TestEmbeddedSchemaInSync fails CI if
// the two ever diverge.
//
//go:embed contract_schema.json
var contractSchemaV1 []byte

// runValidate checks documents against the embedded contract schema
// (schemas/output-contract-v1.json; docs/specs/output-contract.md,
// Testing & acceptance). Tolerant-reader posture: unknown additive fields
// pass; enum and required-key violations fail with pointed paths.
func runValidate(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("validate", flag.ContinueOnError)
	fs.SetOutput(stderr)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	targets := fs.Args()
	if len(targets) == 0 {
		fmt.Fprintln(stderr, "usage: alertlint validate <document.json|corpus-dir>...")
		return 2
	}

	schema, err := compileContractSchema()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}

	var files []string
	for _, target := range targets {
		info, err := os.Stat(target)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		if info.IsDir() {
			matches, err := filepath.Glob(filepath.Join(target, "*.json"))
			if err != nil {
				fmt.Fprintln(stderr, err)
				return 1
			}
			files = append(files, matches...)
		} else {
			files = append(files, target)
		}
	}
	sort.Strings(files)
	if len(files) == 0 {
		fmt.Fprintln(stderr, "alertlint validate: no documents found")
		return 2
	}

	failed := 0
	for _, file := range files {
		raw, err := os.ReadFile(file)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
		if err != nil {
			fmt.Fprintf(stderr, "INVALID %s: not JSON: %v\n", file, err)
			failed++
			continue
		}
		if err := schema.Validate(doc); err != nil {
			fmt.Fprintf(stderr, "INVALID %s:\n%v\n", file, err)
			failed++
			continue
		}
		fmt.Fprintf(stdout, "ok %s\n", file)
	}
	if failed > 0 {
		fmt.Fprintf(stderr, "%d of %d document(s) invalid\n", failed, len(files))
		return 1
	}
	return 0
}

func compileContractSchema() (*jsonschema.Schema, error) {
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(contractSchemaV1))
	if err != nil {
		return nil, fmt.Errorf("embedded contract schema: %w", err)
	}
	c := jsonschema.NewCompiler()
	if err := c.AddResource("contract.json", doc); err != nil {
		return nil, err
	}
	return c.Compile("contract.json")
}
