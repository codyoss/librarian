# Morph

`morph` is a package that provides tools to generate and transform requests for the Librarian system. It leverages Generative AI to create sample requests and can transform existing requests into different formats, such as `curl` commands.

## Features

### Generate Request

The `generate-request` command uses Generative AI (Gemini) to create a valid JSON request body for a specified API method. It analyzes the API definition and JSON schema to produce a realistic sample request.

**Usage:**

```bash
librarian morph generate-request --method <MethodID> [--context <AdditionalContext>]
```

**Flags:**
- `--method`: The ID of the method to generate a request for (e.g., `google.example.v1.ExampleService.CreateExample`).
- `--context`: (Optional) Additional context to provide to the AI model to guide the generation (e.g., "Create a resource with specific field values").
- `--googleapis-root`: The root directory of the googleapis repository.
- `--protobuf-root`: The root directory of the protobuf repository.
- `--spec-source`: The directory containing the service specification (relative to `googleapis-root`).

### Morph Request (Curl Generation)

The main `morph` command reads an existing request file (in JSON format) and transforms it into a `curl` command. This is useful for testing and verifying API interactions from the command line.

**Usage:**

```bash
librarian morph --request <RequestFile> --method <MethodID>
```

**Flags:**
- `--request`: Path to the file containing the JSON request body.
- `--method`: The ID of the method the request is intended for.
- `--output-type`: (Optional) The type of output to generate (currently defaults to `curl`).
- `--googleapis-root`: The root directory of the googleapis repository.
- `--protobuf-root`: The root directory of the protobuf repository.
- `--spec-source`: The directory containing the service specification (relative to `googleapis-root`).

## Example Usage

```bash

# Generate a request
go run github.com/googleapis/librarian/cmd/morph \
  generate-request \
  -googleapis-root="/usr/local/google/home/codyoss/oss/googleapis" \
  -protobuf-root="/usr/local/google/home/codyoss/oss/protobuf" \
  -spec-source="google/cloud/secretmanager/v1" \
  -method=".google.cloud.secretmanager.v1.SecretManagerService.CreateSecret" \
  -context="Use codyoss-workspace for project substitutions"

go run github.com/googleapis/librarian/cmd/morph \
  -googleapis-root="/usr/local/google/home/codyoss/oss/googleapis" \
  -protobuf-root="/usr/local/google/home/codyoss/oss/protobuf" \
  -spec-source="google/cloud/secretmanager/v1" \
  -method=".google.cloud.secretmanager.v1.SecretManagerService.CreateSecret" \
  -request=/usr/local/google/home/codyoss/oss/librarian/out/request.json
```
