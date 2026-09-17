---
id: "CERB-CAP-605"
class: "capability"
name: "Config registration and validation"
summary: "Validation is schema-only and passes every descriptor in the estate, including nine that point at a user who does not exist on this machine."
state_field: "maturity"
state_label: "partial"
review_status: "draft"
confidence_score: 0.95
confidence_label: "all nine descriptors validated live against the installed binary"
last_reviewed: "2026-09-17"
created_at: "2026-09-17"
namespace: "cerberus"
locus: "core"
pointer_locator: "internal/registry/"
tags:
  - "cerberus"
  - "class:capability"
  - "operational-reality"
  - "registry"
  - "validation"
  - "config"
  - "locus:core"
relationships:
  - type: "depends_on"
    target: "CERB-CAP-600"
    note: "the estate this would have registered"
  - type: "relates_to"
    target: "CERB-GAP-638"
    note: "validate does not check that dir: resolves"
  - type: "relates_to"
    target: "CERB-DEC-670"
    note: "why resources are in the global config instead"
  - type: "relates_to"
    target: "CERB-CAP-502"
    note: "the registry mechanism; this record is the operational fact that nothing is registered here"
  - type: "relates_to"
    target: "CERB-CAP-503"
    note: "the validation mechanism whose blind spots this record observes in production"
---

# Config registration and validation

`cerberus validate` with no argument validates the registered set and reports
"Config OK: 0 registered, resolved to 8 projects, 10 resources". With a path it
validates that one file. Both work and both are read-only.

The finding is what validation does not do. Every one of the nine
`*.cerberus.yaml` descriptors in the estate validates `OK`, including all eight
whose every `dir:` is under `/Users/chrispian` — a home directory that does not
exist on this machine, as `ls -d /Users/chrispian` confirms. Validation is
schema-only. It has a guard for the one value that has burned this project
before, `port: 0`, held both by `findPIDByPort` and by
`TestValidateProjectConfigPortZeroIsError`, but nothing checks that a declared
working directory, command or health endpoint resolves.

That is the gate that could have caught the whole "these descriptors are
templates, not configuration" problem at the moment somebody tried to use one,
and it reports success instead. A `--check-paths` mode, or a plain registerability
probe, would turn "why did `cerberus register` do nothing useful" into a
sentence the tool says by itself.

Two smaller sharp edges sit next to it. Passing a descriptor as `--config`
rather than as the positional path produces `unsupported config version: 0
(Cerberus now requires version: 2)` — technically true, since descriptors carry
no `version:`, but it reads as a version problem rather than as "that is not a
global config". And `cerberus config` has only a `validate` subcommand: there is
no way to print the effective merged configuration, so `cerberus resource list`
and `cerberus project list` are the only way to see what the runtime actually
believes.
