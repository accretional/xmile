# Gluon upstream — potential changes (running log)

Candidate changes to `../gluon/v2` that the xmile redesign (ADR 0008) might want.
**None is required to start** — each has an xmile-side workaround. The rule:
build in xmile first, promote to gluon only when a *second* consumer would share
it. Substrate (universal AST ops, lowering, codegen) is gluon's job; domain
(XSD/DTD semantics, OPC packaging) is xmile's.

Status legend: `deferred` (worth doing, not now) · `required` (blocks a phase) ·
`done`.

---

## Verified facts (the ones that decided "no change needed now")

- **`compiler.Compile` is closed-vocabulary.** It lowers only the kinds in
  `compiler/kinds.go`; `emitField` ends in `default: unknown node kind`
  (`compiler/compiler.go:250`) and the doc says *"Any other kind at any position
  is an error."* Constraint kinds cannot ride through it.
- **The hooks observe, they do not mutate.** `OnField(parentFQN, fieldName,
  node)` and `OnMessage(fqn, node)` receive the *un-peeled original* node (so
  wrapper metadata survives, à la `CollapseCommaList`'s stashed separator) but
  return nothing and get no handle to the emitted `FieldDescriptorProto`.
- **Deriving a reduced lowering view before `Compile` is idiomatic** —
  `strip_keywords.go`, and xmile's own `scalarizeUndeclared` (`schema.go:127`).

Together these mean xmile carries constraints in a parallel canonical schema-AST
+ a hook-built side-table, with no gluon change. Items below would only make that
cleaner or unlock Phase 6.

---

## 1. Descriptor-mutating / returning compile hook — `deferred`

**Want:** `OnField` able to attach `FieldOptions` (or return them) so xmile can
serialize constraints (`occurs`, facets, identity) **into** the
`FileDescriptorProto`, rather than only into a Go side-table.

**Why:** lets a compiled schema's constraints travel with the `.proto`/
`FileDescriptorSet` to external consumers, not just the in-process validator.

**Workaround:** post-process the `FileDescriptorProto` xmile-side, using the
side-table the hook already builds, to set custom field options.

**Trigger to revisit:** a consumer outside this process needs the constraints
serialized (e.g. a generated static validator in another language).

## 2. Opaque-annotation passthrough in `Compile` — `deferred`

**Want:** `Compile` ignores (and forwards to the hooks) child kinds it does not
recognize, instead of erroring, so the **canonical** schema-AST could be compiled
directly without first deriving a lowering view.

**Why:** removes the derive-lowering-view step; one tree instead of tree +
projection.

**Workaround:** derive the lowering view (Phase 1) — idiomatic and cheap.

**Trigger to revisit:** if maintaining the lowering-view derivation across many
front-ends proves error-prone.

## 3. Typed-facade codegen over an `ASTNode` dialect — `deferred` (gates ADR 0008 Phase 6)

**Want:** generate typed Go accessors/wrappers for a closed kind-vocabulary
(generic XML; the schema dialect), so engine code written against the facade gets
`go build` type-checking while the storage stays `ASTNode`. The gradual-typing
"Flavor B" — the protobuf pattern (homogeneous wire + generated typed structs)
applied to gluon ASTs.

**Why:** the only way to recover compile-time safety for xmile's engine once
instances become `ASTNode` (ADR 0008 §7). Without it, the engine is stringly-typed
and checks are runtime/coverage-dependent.

**Workaround:** none that restores static safety; either stay Tag-internal
(don't do Phase 6) or accept the stringly-typed engine.

**Trigger to revisit:** committing to Phase 6 (instances as `ASTNode`), driven by
DOCX needing to attach derived data.

## 4. Reusable AST-schema validator — `deferred`

**Want:** a gluon capability to validate an `ASTNode` tree against a kind-schema
("an `element` may contain `attribute`/`text`/`element`…") — the same machine as
xmile's document validator, one level up (gradual-typing "Flavor A", runtime).

**Why:** would make the stringly-typed substrate self-checking and is reusable
beyond XML.

**Workaround:** xmile's generic validator (ADR 0008 Phase 2) does this for the
document layer; apply the same walk to the AST layer in xmile if needed.

**Trigger to revisit:** a second gluon consumer needs AST-shape validation, or the
xmile validator proves cleanly generic.

## 5. Richer `astkit` transform combinators — `deferred` (discover in Phase 4)

**Want:** higher-level rewrite combinators if XSD normalization (derivation
flattening, substitution-group expansion) makes raw `Find`/`ReplaceKind`/`Filter`
unwieldy.

**Why:** XSD→schema-AST may need structural rewrites beyond the current
predicate-based primitives.

**Workaround:** compose the existing primitives; keep xmile-specific transforms in
xmile.

**Trigger to revisit:** during ADR 0008 Phase 4, if the XSD front-end accretes
transform boilerplate worth lifting into gluon.
</content>
