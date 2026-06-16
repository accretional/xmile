# ADR 0001 — Tag-name matching in the XML parser

- **Status:** Accepted
- **Date:** 2026-06-15
- **Decision:** Option A — enforce start/end tag-name equality in a mandatory
  post-parse well-formedness walk, exposed as an intrinsic part of the parse
  contract (`ParseXML` returns a not-well-formed error on mismatch).
- **Follow-up (pencilled in):** Option C — a declarative *matched-named-scoper*
  primitive in gluon — as a later, additive enhancement once the contract and
  the W3C conformance corpus are green.

---

## 1. Context

### 1.1 The constraint

XML well-formedness requires that every element's **end-tag name equals its
start-tag name**:

```xml
<foo> … </foo>     ✓ well-formed
<foo> … </bar>     ✗ not well-formed   (Element Type Match WFC)
```

This is a *well-formedness* constraint (WFC), not a *validity* constraint (VC):
it holds for **every** XML document, with or without a DTD, and a violation is
**fatal** — the input is not XML at all. It is therefore unconditional and must
be enforced by the parser itself, independent of any schema layer.

### 1.2 Why it is hard: tag matching is not context-free

A tag name is an *unbounded identifier*. The rule "the closing name must equal
the opening name" is an equality constraint between two arbitrary-length tokens.
A context-free grammar (EBNF) cannot express "this terminal equals that earlier
terminal" — it can only describe shapes, not cross-token equalities. So no EBNF
production can encode tag matching. (This is the classic `ww` / matched-name
problem; `aⁿbⁿ` is context-free, but "an identifier followed later by the *same*
identifier" is not.)

The same flavour of non-CFG-ness shows up in two sibling well-formedness
constraints, which share this document's fate:

- **Unique Att Spec** — no attribute name may repeat within one start-tag
  (`<a x="1" x="2"/>` is not well-formed). Uniqueness over unbounded names.
- **Single root element** — exactly one top-level element.

None of these can live in the grammar; all are tree-level checks.

### 1.3 The decomposition that makes it tractable

Tag *nesting* and tag *naming* are separable:

- **Nesting structure** — the tree shape comes purely from `<` / `</` / `>`
  balance. That is bracket-matching, which **is** context-free. The grammar's
  recursion plus gluon's *EOF-complete-consumption* rule already enforce it:
  an unclosed tag fails to find its end-tag at EOF; an extra end-tag is left as
  unconsumed input and rejected.
- **Name equality** — the single non-CFG residue. It can be checked
  *independently* of how the tree was built, because every element node can
  carry both its start-name and end-name as data.

So "tag matching" reduces to one question: **where do we assert
start-name == end-name?**

### 1.4 Relevant gluon engine facts

These shaped the option analysis (see `lexkit/parse_ast.go`):

- The v2 `CST` RPC is a **shim**: it serialises the v2 grammar back to EBNF
  *text* (`printExpressions`) and re-parses with v1 `lexkit.ParseAST`. Any new
  *grammar primitive* must survive that text round-trip.
- The engine already exposes `ASTParseOptions.TokenMatchers` — a production can
  be matched by a custom Go function `(src, pos) → (text, newPos)` instead of
  grammar recursion (`tryProduction` consults it first). This is the intended
  hook for lexical constructs a CFG cannot express.
- `matchSequence` **backtracks** (saves/restores position on failure);
  `matchOptional` / `matchRepetition` **do not** backtrack across a surrounding
  sequence. The engine has **no parser-level stack**, and matchers that mutate
  shared state are hazardous under backtracking.

---

## 2. Options

Each option is described by its **mechanism**, **where in the flow** the check
fires, a **worked example**, and its trade-offs.

Grammar shape assumed throughout (token-matcher productions in *italics*):

```ebnf
document   = prolog , element , { misc } ;
element    = empty_elem_tag | ( start_tag , content , end_tag ) ;
start_tag  = "<" , Name , { attribute } , ">" ;
end_tag    = "</" , Name , ">" ;
content    = { element | char_data | reference | cdata | comment | pi } ;
```

where `Name`, `char_data`, `reference`, `cdata`, `comment`, `pi` are token
matchers (lexical scanners), not recursive productions.

### Option A — post-parse well-formedness walk  ✅ chosen

**Mechanism.** The grammar treats `start_tag`'s and `end_tag`'s `Name` as
*independent* identifiers, captured into the AST as the element node's
`start_name` / `end_name`. The grammar therefore over-accepts `<a></b>`. After
the tree is built, a mandatory walk visits every element node and asserts
`start_name == end_name`; a mismatch returns a fatal not-well-formed error. The
walk is wired into the parse entry point, so callers see "the parser rejected
malformed input" — the post-parse timing is an internal detail.

**Where in the flow.**

```
source ─▶ [grammar parse]  ─▶ AST (may contain mismatches)
                              │
                              ▼
                       [WF walk: name-equality, dup-attrs, single-root]  ◀── mandatory, always runs
                              │
                  ok ─────────┴───────── mismatch ─▶ not-well-formed error
                  │
                  ▼
            well-formed AST
```

**Example — `<a></b>`.**

1. Parse: `start_tag` matches `<a>` (Name `a`); `content` is empty (next is
   `</`); `end_tag` matches `</b>` (Name `b`). Element node built:
   `{ start_name: "a", end_name: "b", children: [] }`. Parse **succeeds**, input
   fully consumed.
2. WF walk: element `start_name "a" ≠ end_name "b"` → **not-well-formed**.
   `ParseXML` returns the error.

**Pros.** No parser state; immune to the engine's backtracking; trivial
recursive check; zero gluon-engine change (backward-compat by construction). The
WF walk is needed *anyway* for dup-attrs and single-root, so name-equality rides
along for free.

**Cons.** The intermediate tree momentarily holds mismatched names before the
walk rejects them. Enforcement is "one step after" parsing rather than during it
(no observable difference for a tree-producing parser).

### Option B — stateful token-matcher stack (parse-time, consumer code)

**Mechanism.** `start_tag` and `end_tag` are token matchers closing over a
**shared stack**. `start_tag` pushes the captured name; `end_tag` pops and
returns no-match on inequality, so the grammar fails immediately.

**Where in the flow.** Inside the grammar parse, at the moment `end_tag` is
matched — no separate pass.

**Example — `<a></b>`.**

1. `start_tag` matches `<a>`, **pushes** `"a"`.
2. `content` empty.
3. `end_tag` reads `</b>`, **pops** `"a"`, compares to `"b"` → mismatch →
   returns `newPos = -1` (no match).
4. The `element` sequence fails and the engine **backtracks** — but the stack
   was already popped on a path now being unwound. The shared stack is left
   inconsistent (the `"a"` frame is gone even though that parse path was
   abandoned). Subsequent alternatives see a corrupted stack.

**Pros.** Immediate rejection; no malformed tree ever built.

**Cons.** gluon's engine backtracks and exposes **no hook to roll back a
matcher's side effects**, so the shared stack corrupts under backtracking.
Couples proto-xml to engine internals. Fragile.

### Option C — engine-native *matched-named-scoper* (gluon proto + engine)  🔜 follow-up

**Mechanism.** Add a new grammar/lex primitive: a scoper whose open **captures**
an identifier and whose close **must equal** it. The engine owns the stack and
enforces equality during the parse, correctly saving/restoring the stack across
backtracking (because the engine controls backtracking, it can do this right).
The grammar declares the constraint:

```ebnf
element = <named-scoper open="<" Name ">" close="</" Name ">"> content </named-scoper> ;
```

**Where in the flow.** Inside the grammar parse, declaratively, at the close.

**Example — `<a></b>`.**

1. Engine matches the open scoper `<a>`, captures `a`, pushes on the
   **engine-owned** stack.
2. `content` empty.
3. Engine matches the close scoper, requires its name equal the stack top `a`;
   sees `b` → **parse error at the `</b>` offset**. On backtracking the engine
   restores its own stack, so no corruption.

**Pros.** Declarative in the grammar; parse-time (enables streaming/SAX-style
enforcement); a genuinely reusable, universal primitive — heredocs
(`<<EOF … EOF`), fenced code blocks, labelled `BEGIN … END`. Right home for the
constraint long-term.

**Cons.** Real surgery on a **shared** engine; the new primitive must survive
the brittle v2→v1 EBNF-text round-trip (new text syntax + new `Expr` kind + new
matcher); highest backward-compat risk for `proto-sqlite` / `proto-http` /
`proto-ip`. Slowest path.

### Option D — dedicated structural pre-pass

**Mechanism.** A separate, hand-rolled scanner matches `<tag>` / `</tag>` via
its own stack (names enforced there) and builds the element-span tree **before**
the grammar engine fills in details. It is Option B's stack moved into a pass
that owns its own control flow — no backtracking, so the stack is safe.

**Where in the flow.** A stage *before/instead of* the grammar engine.

**Example — `<a></b>`.** The pre-pass tokenises, pushes `a` on `<a>`, on `</b>`
pops `a` and compares → mismatch → **not-well-formed**, immediately. The grammar
engine is never involved in matching.

**Pros.** Safe stack; early enforcement. **Cons.** A bespoke parser stage that
sidesteps gluon's grammar engine for matching — more code, and it abandons the
"stay grammar-driven" goal.

### Rejected outright — HTML-style recovery

HTML tolerates mismatched/omitted end tags and auto-closes. XML does **not** —
such inputs are fatal by spec. We *want* to reject, so error-recovery matching is
out of scope.

---

## 3. Worked examples across the option space

How each canonical input is caught, and **where**:

| Input | Defect | Option A | Option C |
|---|---|---|---|
| `<a><b>hi</b></a>` | (well-formed) | accepted | accepted |
| `<a></b>` | name mismatch | **WF walk** | **parse** (`</b>`) |
| `<a><b></a></b>` | improper overlap | **WF walk** (a: a≠b; b: b≠a) | **parse** (`</a>`) |
| `<a><b></b>` | `a` unclosed | **parse** (end-tag missing at EOF) | **parse** |
| `<a></a></b>` | extra end-tag | **parse** (unconsumed input) | **parse** |
| `<a x="1" x="2"/>` | duplicate attr | **WF walk** (dup-attrs) | **WF walk** (dup-attrs) |
| (two top-level elements) | multiple roots | **parse**/​**WF walk** | **parse**/​**WF walk** |

Two observations this table makes concrete:

1. Under **A**, structural defects (unclosed, extra end-tag) are *already* caught
   at **parse time** by the grammar recursion + EOF rule; only **name-equality**
   needs the walk. Overlap (`<a><b></a></b>`) parses structurally but is rejected
   by the walk because it forces a name mismatch — A handles it correctly.
2. **C does not eliminate the WF walk.** Duplicate-attribute and single-root are
   not expressible as a named-scoper and still require the tree pass. C only
   *relocates name-equality* from the walk into the engine. So the walk exists
   under either option, which is what makes "A now, C later" low-waste.

---

## 4. Decision and rationale

**Chosen: Option A**, with the name-equality check wired as a **mandatory,
always-run finalisation of the parse** (not as a step hanging off the optional
DTD/validity walk). The parse contract becomes: *`ParseXML` rejects
non-well-formed input.* That is the same observable contract Option C would give;
the difference (in-engine vs. one-pass-later) is internal and invisible to
callers.

Why A over the alternatives, given the constraints:

- **Behaviourally identical to C, today.** For a tree-producing parser, A and C
  reject exactly the same documents with comparable error locality (AST nodes
  carry `SourceLocation`). C buys conceptual purity and a reusable primitive —
  not functional capability — *right now*.
- **The WF walk has to exist regardless** (dup-attrs, single-root), so putting
  name-equality there is not throwaway work; C is purely additive on top of it.
- **Lowest risk to a shared library.** A is self-contained in proto-xml with
  zero gluon-engine change; C is the highest backward-compat risk (shared
  engine + EBNF-text round-trip). Doing C later, *with the W3C corpus green as a
  regression net*, is far safer than doing it blind.
- **B is unsafe** (stack corruption under backtracking); **D abandons the
  grammar-driven approach** for a bespoke stage.

**Option C remains the principled long-term home** and is pencilled in as an
additive follow-up: a matched-named-scoper is a real missing primitive for a
universal parsing kit (heredocs, fenced blocks, labelled blocks), and it would
let name-equality be enforced declaratively and at parse time. It is deferred,
not rejected.

---

## 5. Consequences

- **Parse contract.** `ParseXML(src) → (AST, error)` returns a not-well-formed
  error for tag-name mismatch; callers never receive a malformed tree.
- **The WF walk is the always-on floor** of the post-parse pipeline and owns the
  tree-dependent well-formedness checks:
  - tag-name equality (this ADR),
  - **no duplicate attributes** within a start-tag,
  - **single root element**,
  - (with the entity table) **undeclared general entity → not-wf** in the
    standalone / no-external-subset case.
- **Error taxonomy.** The WF walk emits **not-well-formed**; the later DTD layer
  emits **invalid**. Keeping them distinct is what lets us classify against the
  W3C conformance suite (`not-wf` vs `invalid` vs `valid`) and lets DTD-less
  documents still parse.
- **gluon stays unchanged in its engine** for this feature. The only gluon change
  in this milestone is an *additive* v2 API to thread custom token matchers
  through to `lexkit.ParseASTWithOptions` (covered by its own change; backward
  compatibility verified by gluon's existing test suite staying green).
- **Follow-up tracked:** Option C (matched-named-scoper) as an additive gluon
  primitive after the corpus is green.
