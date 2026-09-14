---
name: sidekick-add-language
description:
  Use this skill when adding a new target language to the sidekick code
  generator, or when modifying an existing sidekick codec (rust, dart, swift,
  golang). Covers the codec contract, the mustache template rules, the
  annotation pattern, and the determinism requirements.
---

# Adding a sidekick language codec

Sidekick turns a language-neutral model (`internal/sidekick/api`) into source
files via mustache templates. This skill describes how a codec plugs in.

## Architecture

Each language spans two layers. Keep the split.

| Layer | Path | Responsibility |
|---|---|---|
| Orchestration | `internal/librarian/<lang>/` | `librarian.yaml` to `parser.ModelConfig`, external tooling, format, tidy, publish |
| Codec | `internal/sidekick/<lang>/` | `api.API` to files, via mustache |

## There is no codec interface

This surprises people, so do not go looking for one. The codecs are
free-standing functions with deliberately different signatures:

```go
rust.Generate(ctx, model *api.API, outdir string, cfg *parser.ModelConfig) error
rust_prost.Generate(ctx, model, outdir, template string, cfg *parser.ModelConfig) error
dart.Generate(ctx, model, outdir string, codec map[string]string) error
swift.Generate(ctx, model, outdir string, library *config.Library, module *config.SwiftModule) error
codec_sample.Generate(ctx, model, outdir string, cfg *parser.ModelConfig) error
```

Adding a new `Generate` function is therefore **not** an interface change and
does not affect any other language. Prefer the `cfg *parser.ModelConfig` shape
unless you have a concrete reason not to.

`internal/sidekick/codec_sample` is the minimal working example. Start there.

## Template rules

Rendering happens in exactly one place: `internal/sidekick/language/generate.go`.

Five entry points, differing only in what the template's root context is:

- `GenerateFromModel` - root is `api.API`, one render per template
- `GenerateService`, `GenerateMethod`, `GenerateMessage`, `GenerateEnum` - root
  is that element, so you drive the loop yourself and call once per element

Use `GenerateFromModel` when one output file covers the whole API (Rust, Go).
Use the per-element entry points when the language convention is one file per
type (Java, C++, and Go's per-service client files).

### The naming rule that silently eats templates

`internal/sidekick/language/walk_templates_dir.go`:

- Basename with exactly **two** dots renders to a file, with `.mustache`
  stripped. `doc.go.mustache` produces `doc.go`.
- Basename with exactly **one** dot is a **partial** and is silently skipped.
  `_header.mustache` is never rendered on its own.

A template that produces no output and no error is almost always this rule.

### Partials

`mustache.RenderPartials` resolves `{{> name}}` relative to the including
template's directory. A leading `/` makes the path absolute within the embedded
FS.

## Language-specific data: the annotation pattern

Every `api.*` type has a `Codec any` field. The established pattern is:

1. Add an annotate phase at the top of your `Generate` that walks the model and
   attaches a language-specific struct to each element's `Codec` field.
2. Templates reach it as `{{Codec.Whatever}}`.
3. "Helpers" are zero-argument methods on your annotation struct. There is no
   registered-helper mechanism, and you do not need one.

See `internal/sidekick/swift/annotate_*.go` for the fullest worked example, one
file per element type.

**Do not add fields to the shared `api.*` types** to carry language-specific
data. That is what `Codec` is for, and shared-IR changes ripple into every other
language. If you genuinely believe the shared IR must change, that is a design
discussion to have explicitly, not a change to slip into a codec CL.

**Do not add a parser-stage transform.** The pipeline in
`internal/sidekick/parser/parser.go` is fixed, unconditional, and
language-neutral. `ModelConfig.Language` exists but is never read. Do your
transformation in your own annotate phase.

## Determinism is a hard requirement

Generated output must be byte-identical across runs.

- **Never range over a map to produce output.** Go randomizes map iteration
  order. Collect keys into a slice, sort, then iterate.
- `api.API`'s `AllMessages()` and `AllEnums()` iterate maps. Never emit output
  directly from them.
- Never read the wall clock. Copyright years and similar come from config. A
  codec that calls `time.Now` stops reproducing its own golden files the moment
  the year rolls over.
- Never read environment variables or absolute paths into output.

A cheap and effective guard is a test that renders twice and compares. See
`TestDeterministicRender` in `internal/sidekick/golang/generate_test.go`.

## Repo constraints

From `AGENTS.md`, and each of these will bite you:

- **No new dependencies.**
- `internal/config` is pure data types. No functions, no methods.
- Use `internal/yaml`, not `go.yaml.in/yaml/v3`.
- In `internal/sidekick/parser`, use the bridged aliases in
  `protobuf_imports_oss.go` / `protobuf_imports_google3.go`. Importing
  `google.golang.org/genproto/...` or `iampb` directly breaks the google3 build.
- Check `internal/testhelper` before writing test utilities.
- New `.mustache` files need a `{{! ... }}` Apache license block, or
  `go tool addlicense -check` fails.

## Verification

```bash
gofmt -s -w .
go tool goimports -w .
go tool golangci-lint run
go test -short ./...
go tool addlicense -check .
go mod tidy -diff
```

Tests that call `parser.CreateModel` shell out to `protoc`, which is usually not
on `PATH`. Those tests **skip** rather than fail, so a broken codec can look
like a passing run. Put the pinned protoc from
`~/.cache/librarian/bin/protoc/*/bin` on `PATH`, or feed pre-built descriptor
sets via `ModelConfig.DescriptorFiles` to avoid protoc entirely.
