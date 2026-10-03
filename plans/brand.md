# xbin brand: Base Two

> Status: **live**: the brand's source of truth (D183, 3 October 2026). The site's
> tokens are `website/css/tokens.css`; the mark's master files and their rules are in
> `plans/brand/marks/`. The product's theme, Concrete Day and Concrete Night (§14,
> values in §15), lands separately as **D184**. Measured figures come only from the
> claims register, `hack/demo/measurements.md`.

---

## 0. The idea

Read the name as a formula: **x** is the variable, **bin** is binary, base two. xbin
announces the exponential era the way eras have always been announced: on posters and
signs, in big type on bold colour, set against concrete. Four colour fields double in
size, **yellow 1, green 2, magenta 4, cobalt 8**, and the biggest statement gets the
biggest field.

The announcement is loud. Everything after it is quiet, exact and checkable. **Loud
headline, sober proof.** That pairing is the brand, and it is how xbin earns trust from
builders and from the IT people who approve them.

The 1970s reference is specific: transit signage, exhibition posters, poured concrete,
enamel sign plates, colour used as information. It is never nostalgia: no wood grain, no
retro fonts, no orange-brown sunsets, no film-grain kitsch beyond a light print texture
on colour fields.

---

## 1. Positioning

**The thesis.** A company is a set of systems that get things made. Agents should build
those systems. People and agents build them, and no longer operate them. Putting an
agent in a seat to do a person's job only adds one more thing to watch; the brand never
frames agents that way.

**The definition** (one plain sentence, used verbatim): *xbin is a workspace where people
and AI agents build the systems a company runs on, and where those systems run.*

**The story** (film and home page): how much software was made from the 1950s to today;
the bend after the first large language models; the curve carried forward, clearly
labelled as a projection; back to today, where the old ways break under how fast and how
much agents can make; why companies need a new way; xbin already doing it. Keep the
exponential emotion. Tell why, and leave how to the product page. History figures come
only from the research pass; until then the copy carries `{{DATA: …}}` slots.

**Deployment mode is never mentioned.** No surface says where xbin runs or who runs it:
no "self-hosted", no "your server", no "managed", no hosting or operating-system pitch.
The install page states requirements as facts, without a pitch.

**The product plug** is quiet: show xbin working (a save going live, an agent's change,
a grant as a line in a file). Never a feature list in the story.

**The owner's lines** (verbatim, used as given):
- "upgrade to exponential era software" (install heading, campaign line)
- "the first exponential era workspace" (kicker, tagline, App Store promo text)

**The fixed headline:** "Software has entered its exponential era." On the home page it
opens the story; the definition follows the idea.

**Proof pillars** (facts from the docs; figures from the claims register, plain wording):

| Pillar | Line | Proof |
|---|---|---|
| Save, and it is live | Saving a change puts it live. | On a 96-core workstation: a saved page is on screen in under 0.35 s; 0 of 5.39 million reads failed across 60 live updates under load. |
| A system is a folder | Each app is a folder with a page, an optional backend and its own history. | Move to rename, copy to fork; its own identity, grants, encrypted vault and storage. |
| Agents build the systems | Agents build and change apps from a prompt. | A built-in agent with tools, subagents, schedules, triggers and chat channels; Claude Code, Codex, Gemini CLI and opencode, each in its own sandbox, signed in once. |
| Closed until granted | Every app and agent works in its own sandbox and reaches only what is granted. | Outbound network default-deny, approved per destination; grants in a plain, versioned file. |
| A copy for each person | An app can run one copy per person. | Others cannot read it, admins do not open it; on a 96-core workstation a small app's copy starts on first request in under 0.3 s. |
| Ready for IT | The controls IT asks for are built in. | SSO (OIDC, GitHub), organisations and roles, audit log, read-only view-as-user, sealed encrypted backups, per-app limits, public decision log. |

**Dropped claims, never to return without new measurements:** microVM boot and burst
figures (they describe a heavier sandbox than most apps get), backend rebuild times,
terminal open times, install time (never measured).

**Audiences and what each needs to see first**
- Builders: the story, then a save going live.
- Teams and leaders: the thesis, and one place where people and agents build what the
  company runs on.
- IT and security: the mechanism (who checks what, where it is recorded), never adjectives.

---

## 2. Three volumes: where the loudness stops

Every surface belongs to exactly one volume. The volume decides what is allowed. This is
the main rule of the system and the answer to "can IT take this seriously?"

| | **Announce** | **Inform** | **Work** |
|---|---|---|---|
| Where | Home hero, campaign pages, OG cards, film titles, event posters, App Store promo art | Product, Security, Docs, Decision log, Install, pricing, legal, IT one-pagers | The browser shell, admin console, terminals, agent chat, iOS app |
| Colour fields | Full size, up to 4 per screen | One section band per page at most (never on Security, Docs, Legal); part tabs and screenshot mats | Part tabs (3 px), the elevated banner, first run and empty states only |
| The stair | Yes, once per surface | No | First run only |
| Photography | Yes, one photograph per screen | No (screenshots in mats only) | Never |
| Largest type | Stair ×8 (up to about 350 px) | 64 px (h1) | 32 px (empty states) |
| Voice | The statement, then the definition | Facts, one per line, with qualifiers | Verbs on buttons, nouns in titles, errors that say what is still safe |
| Era stripe | 8 px, top of page | 6 px, top of page | None (the product is the workspace, not a poster) |

1. A page never switches volume halfway through, except the home page, which moves
   through all three once and in order: Announce for its first screen; Inform for the
   story (the curve, what breaks, the idea on the page's one field band); Work for xbin
   shown working and measured, the command band and a quiet closing call.
2. Security, Docs and Legal are always Inform. No fields, no stair, no photography.
   Their confidence comes from big ink type, heavy rules, plates and exact facts.
3. The product is always Work. It should feel like the poster's back room: concrete,
   ink, one accent, everything labelled.

---

## 3. Voice

### Rules

1. **Announce, then explain.** Every big line is followed by a plain sentence that a
   non-engineer understands.
2. **Measured numbers only, from the claims register** (`hack/demo/measurements.md`), in
   plain words. The site keeps four: a saved page on screen in under 0.35 s; 0 of 5.39
   million reads failed across 60 live updates; a small app's copy for a person starting
   in under 0.3 s; typing within one frame at a 300 ms round trip. Every claim sentence
   names its machine ("On a 96-core workstation…") and keeps its conditions ("a small
   app"); each page that quotes figures carries the hardware note once. History figures
   come only from the research pass. Never invent, never imply a benchmark, never quote
   install time (not measured).
3. **Print the command.** When an action is a command, show it verbatim in mono,
   copyable, on one line. The trial command comes first, the install command second,
   neither labelled by where xbin runs.
4. **Short declaratives,** present tense, concrete verbs: save, grant, approve, build,
   fork, diff.
5. **Plain part names:** the workspace (in the browser), bx (the command-line tool), the
   iOS app. Explain each once per page.
6. **No jargon on story surfaces** (home, product, iOS, film, social): no microVM,
   Firecracker, namespace, daemon, kernel, container, WASM, and no programming-language
   names. Say "its own sandbox", "a backend", "storage". The security page and the
   install guide may be concrete for IT, but the security page never names the sandbox
   technology.
7. **Never say where xbin runs or who runs it.** No self-hosting, managed or
   operating-system pitch anywhere in the positioning.
8. **Agents are software** that builds and changes apps from a prompt. Never call them
   colleagues, teammates, hires, staff or "your new engineer". Agents "run", "build",
   "change", "ask for a grant"; they do not "join" or "help out".
9. **No comparisons** with other products. No "not X, but Y" constructions. No em dashes.
   No hype words: revolutionary, magic, seamless, effortless, military-grade,
   bulletproof, unhackable.
10. **Confidence from facts and scale,** never from exclamation marks.
11. **Show the mechanism for trust claims:** say what is checked, by whom, and where it
    is recorded. Avoid the word "secure" on its own.

### Per volume

- **Announce:** one statement, one definition, one command. The owner's lines allowed.
- **Inform:** facts in tables and plates; each sentence carries one fact; numbers have
  units and qualifiers; every claim can be traced to the docs every workspace serves.
- **Work:** buttons are verbs ("Approve", "Open terminal"); titles are nouns ("Grants",
  "Audit log"); errors say what happened, what is still safe, what to do next. The
  installer's pattern ("prints its plan and asks first") is the product's pattern for
  every consequential action.

### Vocabulary

| Use | Avoid |
|---|---|
| grant, approve, default-deny, sandbox | allowlist magic, locked down, zero-trust (as a slogan) |
| goes live, swaps in, rebuilds | deploys instantly, ships at the speed of thought |
| agent, coding agent, built-in agent, session | AI teammate, copilot coworker, digital employee, an agent doing a person's job |
| systems, the systems a company runs on, build | operate, run it yourself, keep the lights on |
| its own sandbox | microVM, namespace, container, daemon (on story surfaces) |
| workspace | self-hosted, on-prem, managed, your server, your cloud |
| under 0.35 s, on a 96-core workstation | ~200ms, blazing fast, instant, zero downtime |

### Samples

- **Installer banner:** `xbin installer · upgrade to exponential era software. Below is the plan for this machine. Nothing changes until you answer y.`
- **Error:** `Blocked: invoices tried to reach api.example.com. Outbound network is closed until a destination is granted. Approve this one and the grant is added to your versioned grants file.`
- **App Store subtitle (≤ 30):** `Apps, terminals and agents`
- **OG title:** `xbin: Software has entered its exponential era`
- **iOS welcome:** `Welcome to xbin. Sign in with this iPhone's secure hardware, and your workspace's apps, terminals and agent sessions are right here.`

---

## 4. Colour

### 4.1 The fields (announce)

| Field | Order | Light | Dark | Ink on field | Contrast |
|---|---|---|---|---|---|
| Yellow | 1 | `#FFD000` | `#FFD000` | ink `#0B0C12` | 13.3:1 |
| Green | 2 | `#00A86B` | `#00A86B` | ink `#0B0C12` | 6.3:1 |
| Magenta | 4 | `#DB0072` | `#DB0072` | white | 4.9:1 |
| Cobalt | 8 | `#1F3DFF` | `#3350FF` | white | 6.6:1 / 5.6:1 |

Fields are flat, square, full-bleed where they can be, and carry a light screen-print
grain (an SVG `feTurbulence` noise mapped to black at 0.11 alpha, about 5 % average) so
they read as printed, never as plastic. Text on a field always uses that field's ink.
Fields never take gradients, glows, transparency or rounded corners. Cobalt lifts in dark
(`#3350FF`) so the largest field still separates from the ink page (3.5:1 on `#0B0C12`).

### 4.2 The doubling order (both themes)

**Colour order is size order.** Yellow is always the smallest field, then green at twice
its size, magenta at four times, cobalt at eight. This holds in light and dark, in the
stair, the era stripe, the photography and the mark. The order never flips with the
theme, because the order *is* the doubling.

- The stair, top to bottom: yellow ×1, green ×2, magenta ×4, cobalt ×8.
- The era stripe, left to right: open 1 (outlined in the text colour), yellow 1, green
  2, magenta 4, cobalt 8 (sixteenths of the width).
- Photography: four glass sheets, smallest yellow to largest cobalt.
- With fewer than four fields, keep their relative order and size (green ×1, cobalt ×4).

### 4.3 Concrete (neutrals)

A cool, near-neutral ramp that ends in the brand ink; deliberately not cream, not warm
paper.

`0 #FFFFFF · 25 #F7F8FA · 50 #F1F2F5 · 100 #E8E9EE · 150 #DCDEE4 · 200 #CDD0D8 · 300 #B1B4BF · 400 #9396A4 · 500 #7E8194 · 600 #626576 · 700 #4B4D5C · 800 #33353F · 850 #262730 · 900 #1F2028 · 925 #16171D · 950 #111218 · 1000 #0B0C12`

500 is the lightest grey that holds 3:1 on white (component borders). 700 is the muted
text colour (8.3:1).

### 4.4 The page, light and dark

| Token | Light | Dark | Note |
|---|---|---|---|
| bg | `#FFFFFF` | `#0B0C12` | |
| surface / surface 2 / surface 3 | `#F1F2F5` / `#E8E9EE` / `#DCDEE4` | `#16171D` / `#1F2028` / `#262730` | |
| text | `#0B0C12` | `#F3F4F8` | 19.5:1 / 18.1:1 |
| muted / subtle | `#4B4D5C` / `#626576` | `#A3A6B6` / `#8A8D9E` | muted 8.35:1 / 8.1:1 |
| border / divider | `#7E8194` / `#CDD0D8` | `#666A7E` / `#2C2E38` | border 3.85:1 / 3.66:1 |
| accent / hover / ink | `#1F3DFF` / `#1530D6` / `#FFFFFF` | `#8C9BFF` / `#A9B4FF` / `#0B0C12` | 6.63:1 / 7.68:1 |
| focus / focus gap | `#0086A6` / `#FFFFFF` | `#3DD6F5` / `#0B0C12` | |
| ok, warn, danger, info | `#436C0C` `#9A4A06` `#C81E1E` `#3D4A5C` | `#A3CF5E` `#F2994A` `#FF7A7A` `#A9B4C6` | each ≥ 5.7:1 |
| ok, warn, danger, info backgrounds | `#EEF5E1` `#FBEEDF` `#FBE7E7` `#ECEEF2` | `#1C2612` `#2B1D10` `#2E1416` `#1C2029` | |
| band, its text, soft text, rule | `#0B0C12` `#FFFFFF` `#B4B7C8` `#2A2D40` | `#3350FF` `#FFFFFF` `#E8EBFF` `#5A72FF` | the command band |
| band prompt, action, action ink, focus | `#FFD000` `#FFFFFF` `#0B0C12` `#0086A6` | `#FFEB70` `#FFFFFF` `#0B0C12` `#FFFFFF` | |

### 4.5 Accent (action)

The accent is **always the cobalt family**: `#1F3DFF` on light (white label, 6.6:1),
`#8C9BFF` on dark (ink label, 7.7:1). Links, primary buttons, selection, the live
indicator and active tabs use it. Yellow, green and magenta **never mark an action**:
they announce (fields) or label a part (tabs, mats). In the product, yellow means
"elevated", so a yellow button would lie. On bands, actions are white with ink labels.

### 4.6 Focus

**Focus is cyan:** `#0086A6` on light (4.2:1 on white), `#3DD6F5` on dark (11.3:1 on
ink). Cyan is the one hue no field, accent or status uses, so a focus ring can never be
mistaken for a brand colour or a state. Construction: a **3 px ring** in the focus
colour, 2 px outside the element, with a **2 px gap** in the page colour (focus gap), so
the ring shows on any field. On the cobalt band the ring is white.

### 4.7 Status: signals live in the gaps

The four fields own four hues; status colours sit in the gaps between them (danger red
~0°, warn burnt orange ~28°, ok olive ~86°, focus cyan ~192°, info slate desaturated
~215°), darker or softer than the fields, and always come with an icon shape and a word.
Status is a small signal: icon + word + optional tinted background, never a field or a
band. Shapes differ so colour is never the only cue: ok = check in a square, warn =
triangle, danger = octagon, info = "i" in a square. A field colour never signals status:
a green part tab does not mean "ok", a yellow banner means "elevated", never "warning".

### 4.8 Part colours

Screenshots, tabs and product contexts are colour-coded by part, consistently:

| Part | Field | Where it shows |
|---|---|---|
| Browser shell | cobalt | shell screenshots, the shell's own chrome accents |
| bx and terminals | green | terminal windows, CLI screenshots |
| Agents and the iOS app | magenta | agent session windows, the agent chat header, iOS screenshots |
| Admin and elevated | yellow | admin console, the view-as-user banner, approvals that need admin rights |
| xbind | none | concrete neutrals: the ground everything stands on |

### 4.9 Proportions and pairs

- **Announce:** fields up to 60 % of the first screen; paper or ink for the rest; one
  photograph. **Inform:** fields under 2 % of a page (era stripe, part tabs, mats).
  **Work:** fields under 2 % of the screen in normal use; empty states and first run may
  use one field block.
- Allowed text pairs only: ink on yellow, ink on green, white on magenta, white on
  cobalt, ink on paper or concrete 0 to 200, white on ink or concrete 800 to 1000.
- Forbidden: field text on another field, coloured type on photographs, any field as a
  page background in Inform or Work, gradients between fields, field colours at reduced
  opacity as tints.

---

## 5. Type

### 5.1 Families

| Role | Family | Weights | Use |
|---|---|---|---|
| Display | Bricolage Grotesque (OFL) | 600, 800 | The stair, headlines, plate labels, big numbers, wordmark A |
| Text | Instrument Sans (OFL) | 400, 600 | Body, UI, tables, buttons |
| Mono | JetBrains Mono (OFL) | 400, 500, 700 | Commands, paths, code, terminals (500 on the web; 400 and 700 in terminals) |

Fallbacks: display `"Arial Black", "Helvetica Neue", Arial, system-ui, sans-serif`; text
`system-ui, -apple-system, "Segoe UI", Roboto, sans-serif`; mono `ui-monospace,
SFMono-Regular, Menlo, Consolas, monospace`. Self-hosted woff2 only (`website/fonts/`,
with their licences; the latin subsets plus two small symbol subsets for ↗ and ≤ ≥).

### 5.2 Web scale

| Token | Size / line | Family, weight | Notes |
|---|---|---|---|
| micro | 11 / 16 | display 800, caps, +0.09em | labels, `dt`, plate rows |
| caption | 12 / 16 | text 400 | fine print |
| small | 14 / 20 | text 400 | secondary copy |
| body | 16 / 24 | text 400 | default |
| lead | 20 / 28 | text 600 | the definition line |
| h4 | 24 / 30 | display 600 | card titles |
| h3 | 32 / 36 | display 800, -0.02em | section titles in cards |
| h2 | 48 / 50 | display 800, -0.03em | section heads |
| h1 | 64 / 62 | display 800, -0.035em | Inform pages (largest Inform size) |
| display | 96 / 88 | display 800, -0.04em | Announce section bands |
| stair | s ×1, ×2, ×4, ×8 | display 800 | Announce, once per surface |
| command | 20 / 27 | mono 500 | install band; on phones it scales to fit one line, never under 12 px |

Below 600 px: h1 40/40, h2 32/34, display 56/52.

### 5.3 Product scale

| Token | Size / line | Family, weight |
|---|---|---|
| micro | 11 / 14 | text 600, caps, +0.06em |
| meta | 12 / 16 | text 400 |
| ui | 13 / 18 | text 400 (default for controls and lists) |
| body | 14 / 20 | text 400 (agent chat, in-app docs) |
| title | 16 / 22 | text 600 |
| heading | 20 / 26 | display 600 |
| hero | 32 / 36 | display 800 (empty states, first run) |
| mono | 13 / 20 (12 / 18 dense) | mono 400, bold 700 |
| number | 13 / 18 | text 600, tabular figures |

### 5.4 The stair rule

The stair is the brand's picture: the statement set as type that doubles line by line.

1. Two to four lines, each twice the size of the one above (desktop ×1, ×2, ×4, ×8). On
   phones the width forces ×1, ×1.5, ×2.25, ×4.5; keep the last step a doubling.
2. Each line sits on its own field. Fields bleed to the left page edge; text starts at
   the page margin; each field ends 0.3em after its last letter. Fields touch.
3. Colours in doubling order, top to bottom: yellow, green, magenta, cobalt. Same order
   in both themes.
4. The last line is the climax: the shortest line set the largest ("era.").
5. Fields hug the type (`text-box: trim-both cap alphabetic`, x-height for an
   all-lowercase last line), padding about 0.13em to 0.34em.
6. Tracking tightens with size: -0.01em at ×1 to -0.045em at ×8.
7. One stair per surface, Announce volume only, at most about 45 characters. Never a
   stair for a product claim, a price or a security statement. If a line would overflow
   its column, all lines shrink together; they never grow.
8. A plain definition follows on the same screen. The stair is one `h1`; its lines are
   spans inside it, so it reads as one sentence.

### 5.5 Commands, numbers and case

- Commands are mono, one line, copyable, with the `$` prompt as non-copyable decoration.
  The install command never wraps; on phones it scales to fit and drops the prompt.
- Numbers: tabular figures in tables; a space before the unit (`0.35 s`); qualifiers in
  words ("under", "about"), as the claims register words them.
- Sentence case everywhere. Caps only for micro labels. The name is always lowercase
  "xbin", even at the start of a sentence.
- Measure: 60 to 72 characters for body; 40 for leads.

---

## 6. Layout and grid

- **Web grid:** 12 columns; margins `clamp(16px, 3.34vw, 48px)`; gutters
  `clamp(16px, 2.8vw, 40px)`; content to 1440 px, fields and bands bleed full width.
- **Spacing:** base two with half steps: 4, 8, 12, 16, 24, 32, 48, 64, 96, 128.
- **Corners:** **2 px radius on everything with a visible corner**, on the site and in
  the product: buttons, inputs, code blocks, cards, plates, windows, screenshot mats,
  tabs, menus. No pills, no larger radii. Full-bleed bands and fields show no corners.
  (The owner: "I really like the less rounded corners, we should apply that
  consistently everywhere.")
- **Rules:** 1 px hairlines, 2 px for active states, 3 px for the top of plates, figures
  and spec rows.
- **Bands:** full-bleed strips (the command band, section bands): ink in light, cobalt in
  dark. Bands stack without gaps, like signage.
- **Plates:** the 1970s sign plate: an ink header bar (white display caps, 11 to 13 px,
  +0.09em) over label and value rows separated by hairlines, a 3 px frame. Used for spec
  sheets, security facts, pricing tables. Inverts in dark (white bar, ink label).
- **The home hero:** a diptych, type on paper at left (kicker, stair, one-line lead, the
  two buttons), the glass-hall photograph full height at right with nothing on it. On
  phones a 112 px photo strip under the header, the stair overlapping its lower edge.
- **Inform template:** era stripe 6 px, header, h1 in ink at left with the lead, a plate
  of facts at right; sections open with a label ("01 · Default-deny"), an h2 and one
  paragraph, then a grid of facts.
- **Theme:** follows the visitor's system setting (`prefers-color-scheme`) everywhere;
  no toggle on the site, nothing stored. Every colour comes from tokens, so neither theme
  breaks on a hard-coded value.

---

## 7. Imagery

### 7.1 The glass hall

The house photography: architecture, concrete and coloured light. A white gallery by day
and a dark hall at night, four transparent glass sheets in yellow, green, magenta and
cobalt, each twice the size of the last, laying hard-edged colour on the floor. Day
images go with the light theme, night images with the dark theme. No people, screens,
devices, text or logos; crisp light with hard shadow edges; no haze, fog, glow, neon
tubes or lens flare; medium format look, eye level, sharp throughout, fine grain; a quiet
zone composed in where content will sit.

### 7.2 Commissioned (generated) images

- **Announce volume only:** the home hero, campaign pages, OG cards, posters, film
  plates. One photograph per screen. Never in Inform or Work.
- **Never commission:** product UI or screens, logos or marks, recognisable people or
  brands, text that makes product claims, robots, glowing orbs, sparkles, hockey-stick
  charts, purple-blue gradients.
- **Prompt skeleton:** subject · the four sheets in doubling order and size · crisp hard
  light · the forbidden list · medium format, 35 mm equivalent, eye level · composition
  boxes for the quiet zone. Store the prompt and generation report next to each image
  (`website/art/*.json`).
- A person reviews every image before use. Images that drift (sheets not doubling, haze,
  neon) are regenerated or cropped to the part that holds. The night image
  (`website/art/hall-night.jpg`) is the 2k replacement for the first one, whose sheets
  were roughly equal in size and whose spotlights showed. It is one of four takes; a box
  edit replaced its ceiling truss with more wall, and its report sits beside it.
- Every image has alt text, and a solid fallback (yellow in light, concrete 900 in dark)
  so the layout holds without it.

### 7.3 Data charts

The story's curve is a data chart, drawn in SVG from sourced numbers, never a
commissioned or decorative image. Real, sourced data only, sources printed under the
chart; a linear scale, stated in the caption; an ink line, the area before the
inflection in concrete and after it in cobalt; any projection a dashed cobalt line with
no fill, labelled "If the curve holds" and called a projection in the caption. No arrows,
rockets, glows, gradients or 3D. A data table sits behind every chart.

---

## 8. Screenshot mats

Every product screenshot sits in a solid mat in its part's colour (10 px on the web,
8 px on phones, 2 px outer radius) with a tab in the same colour, top-left, naming the
part (13 px text 600, white on cobalt and magenta, ink on green and yellow). No device
chrome, tilt, perspective, glow or reflections; one soft shadow only on a photograph.
Screenshots are real, current and uncropped at the window level, captured in both
product themes with identical content (the page swaps them with `<picture>`). Alt text
says what the screenshot shows. The mat carries the grain.

---

## 9. Iconography

16 px (dense) and 20 px grids; strokes 1.5 px at 16, 2 px at 20; square caps, mitred
joins; built from squares, rectangles and 45° diagonals; corners square (at most 1 px).
Outline by default; a filled square marks "active" or "live". Part glyphs: shell =
window; terminal = prompt chevron and bar; agents = two linked squares; admin = key;
xbind = stacked rack. Never emoji, sparkles, robot heads or brains.

---

## 10. Motion

1. **Grow once, then hold.** Entrances are one doubling sequence (offsets 80, 160,
   320 ms) that ends in a still poster.
2. **Type is revealed by a left-to-right wipe,** like ink pulled across a screen. Fields
   never fade, pulse or glow.
3. **Easing:** exponential out `cubic-bezier(.16,1,.3,1)`; no bounce, no overshoot.
4. **Durations:** UI 80 to 120 ms; panels 200 ms; windows 240 ms; announce wipes 720 ms.
5. **Nothing loops** without a visible pause control. Calls to action never move.
6. **Product motion** confirms state changes and nothing else: a window rises 8 px as it
   opens; the live square fills when a save goes live.
7. **Reduced motion:** every animation and transition off; the final state at once.

---

## 11. Accessibility

WCAG 2.2 AA everywhere, checked for both themes: body text 4.5:1, large text and UI
3:1. Focus always visible (§4.6). Colour never the only cue. Targets at least 24 × 24 px
on the web, 44 pt on iOS. Commands are real text: selectable, with labelled copy buttons
and a polite live region for "Copied". `prefers-reduced-motion` and
`prefers-contrast: more` honoured (in high contrast, dividers take the text colour and
the grain goes). Every image has alt text; every page holds its layout without images.

---

## 12. Keeping IT credibility

1. **The loudness stops at the end of the home hero.** Below it, at most one field band
   per page, and none on Security, Docs, Legal or the admin console.
2. **Security, Docs, Decision log and Install are Inform volume:** paper or ink,
   concrete, big ink type, plates and tables. The era stripe is the only colour.
3. **Show the mechanism.** Who checks what (xbin checks every call), where it is recorded
   (the audit log, the versioned grants file), what an admin can and cannot see
   (view-as-user is read-only; a person's own copy of an app is not opened by admins).
   "Every app and every agent runs in its own sandbox", never the sandbox technology or
   where xbin runs.
4. **Facts only from the docs, figures only from the claims register,** with their
   qualifiers and their machine. No certifications, compliance badges, uptime figures or
   customer logos unless they exist.
5. **Link the evidence:** the source, the public decision log, the docs every workspace
   serves.
6. **A calm product:** the admin console uses the same concrete and ink as the rest;
   yellow appears only as the part tab and the view-as-user banner.
7. **IT collateral** uses the Inform template and never the stair.

---

## 13. The mark

The owner chose **M3, the bˣ tile**: a white b with a yellow x raised as its exponent,
reversed out of cobalt. Master files, construction, sizes and rules:
`plans/brand/marks/README.md`. Every surface uses the files and never redraws, recolours
or re-letters them; the site loads `website/img/mark.svg` and `website/img/wordmark.svg`
(wordmark A, exact Bricolage 800 outlines; the owner may still swap in B, one file), and
`website/favicon.svg`.

What the mark keeps: it reads as **growth outward** (doubling), never subdivision or
decay; it does not resemble four-colour tile logos (no 2×2 or quartered squares); at
16 px no two light cells touch; one- and two-colour versions work; it works full-bleed
as the iOS icon and on each field.

---

## 14. The product: Base Two at work (lands as D184)

**The website announces; the workspace works.** The product is the Work volume: concrete
surfaces, ink type, one accent (cobalt), and colour fields only where they carry meaning,
comfortable for an eight-hour day on a 13-inch laptop or a 32-inch monitor, in light and
dark. Three tests for every screen: **calm** (field area under 2 % in normal use),
**labelled** (every state named in words, colour as a second cue), **honest** (the real
thing: paths, commands, measured times, grants as text).

- **Density:** compact by default (28 px rows, UI 13/18, 12 px panel padding, terminal
  12/18), comfortable per person (32 px, 14/20, 16 px, 13/20); a 4 px grid. iOS follows
  platform sizes (44 pt targets, Dynamic Type).
- **Shell:** top bar 40 px (wordmark at 16 px, workspace switcher, command palette ⌘K,
  agent activity, account), sidebar 248 px collapsing to a 48 px rail (Apps as the
  folder tree, Agents, Terminals, Admin for admins; micro-caps labels, 28 px rows, the
  selected row in the selection colour with a 2 px accent rule), the canvas on the shell
  background with an 8 px dot grid, the agent chat dock 400 px on the right (⌘J), a 24 px
  status bar. The shell, admin console and app manager are apps: this is the theme they
  ship with.
- **Windows:** a 28 px title bar (the live square, the app's name, its path in mono 12 px
  muted; the live label "live · 214 ms" with xbind's measured time, never an estimate;
  28 × 28 square controls), opaque, 2 px corners, 6 px resize handles, close hover in
  danger. The live square: hollow while building, filled with the accent when live,
  outlined in danger with "failed". A 3 px part tab across the top of system windows only:
  green terminals, magenta agent sessions, yellow admin. User apps get no colour.
- **Where colour appears:** the accent for primary buttons, selection, the active tab,
  links and the live square; part tabs; the yellow elevated banner (28 px, full width of
  the canvas, ink text) for view-as-user and any elevated mode; status colours with icon
  and word; the cyan focus ring; one field block in first run and empty states. Never a
  field behind app content or as a panel background, gradients, field-coloured text,
  status shown by a part colour, or more than one field on screen.
- **Components:** buttons 28/32 px, radius 2, ui 600 (primary accent, secondary panel
  with border-strong, quiet text-only, destructive danger text and border); inputs 28 px
  with border-strong and the focus ring, mono for paths and hostnames; lists and trees
  28 px rows; text tabs with a 2 px accent underline, never pills; admin tables 32 px
  rows, tabular figures, a sticky header with a 2 px text rule; square badges; toasts
  bottom right, at most three; dialogs follow the installer's pattern, **show the plan,
  then ask**, with the action as a verb ("Approve grant").
- **Terminal:** JetBrains Mono 400, bold 700 (bold is weight, never a brighter colour),
  ligatures off, padding 8 × 12 px, a block cursor in the accent, the ANSI palettes in
  §15 (a tool palette: programs choose its colours, so the fields-never-signal rule does
  not bind it).
- **Agent chat:** the agent's name as it is ("Built-in agent", "Claude Code"), the session
  state in words ("working 0:42"), a 3 px magenta rule on top; no faces, avatars, robots
  or sparkles; tool calls collapsed into mono blocks with their exit status; grant
  requests as plates with Approve and Deny; agents are software ("The agent changed 2
  files"); text streams as it arrives, no shimmer.
- **Admin console:** Inform inside the product: tables, plates, concrete and ink, the
  yellow part tab; the audit log, grants as text with a diff view, egress requests with
  Approve and Deny, view-as-user under the yellow banner
  (`Viewing as … · read-only · Exit view`), per-person instances as counts only.
- **iOS:** native idioms, Base Two materials: large titles in Bricolage 800, the system
  font elsewhere, product tokens for surfaces, part tabs as 3 px rules on cards, the
  system appearance; first run is the one place the stair appears in the product.
- **Themes:** both follow the system setting by default; each person can override, and
  terminals can take their own theme. Dark is not inverted light: surfaces step up in
  lightness as they come forward, and shadows get deeper.

---

## 15. Product tokens (Concrete Day, Concrete Night)

| Token | Concrete Day | Concrete Night |
|---|---|---|
| shell background / canvas dot | `#E8E9EE` / `#CDD0D8` | `#0B0C12` / `#1F2028` |
| sidebar / panel / panel 2 / hover | `#F1F2F5` / `#FFFFFF` / `#F7F8FA` / `#EEF0F4` | `#111218` / `#16171D` / `#1C1D24` / `#1F2129` |
| border / border strong | `#CDD0D8` / `#7E8194` | `#2C2E38` / `#666A7E` |
| text / muted / subtle | `#0B0C12` / `#4B4D5C` / `#626576` | `#E9EAF0` / `#A3A6B6` / `#8A8D9E` |
| accent / accent ink | `#1F3DFF` / `#FFFFFF` | `#8C9BFF` / `#0B0C12` |
| selection / selection text | `#DDE2FF` / `#0B0C12` | `#262C5C` / `#E9EAF0` |
| focus | `#0086A6` | `#3DD6F5` |
| ok · warn · danger · info | `#436C0C` · `#9A4A06` · `#C81E1E` · `#3D4A5C` | `#A3CF5E` · `#F2994A` · `#FF7A7A` · `#A9B4C6` |
| part: shell · terminal · agents · admin | `#1F3DFF` · `#00A86B` · `#DB0072` · `#FFD000` | `#3350FF` · `#00A86B` · `#DB0072` · `#FFD000` |
| elevated banner / text | `#FFD000` / `#0B0C12` | `#FFD000` / `#0B0C12` |
| title bar / active | `#F1F2F5` / `#FFFFFF` | `#16171D` / `#1F2028` |
| title text / inactive | `#0B0C12` / `#626576` | `#E9EAF0` / `#8A8D9E` |
| window border / active | `#CDD0D8` / `#33353F` | `#2C2E38` / `#4B4D5C` |
| window shadow | `0 1px 0 rgba(11,12,18,.04), 0 8px 24px rgba(11,12,18,.10)` | `0 10px 28px rgba(0,0,0,.40)` |
| active window shadow | `0 1px 0 rgba(11,12,18,.06), 0 14px 36px rgba(11,12,18,.16)` | `0 16px 40px rgba(0,0,0,.55)` |
| control hover / close hover | `#E8E9EE` / `#C81E1E` | `#262730` / `#FF7A7A` |

**Terminal, Concrete Day** (bg `#FFFFFF`, fg `#1C1D24`, cursor `#1F3DFF`, selection
`#DDE2FF`): black `#1C1D24` / `#5A5D6C`, red `#C81E1E` / `#E0352B`, green `#00794A` /
`#00965C`, yellow `#8A6100` / `#A87800`, blue `#1F3DFF` / `#4A63FF`, magenta `#B0005C` /
`#D4007A`, cyan `#0E7490` / `#0891B2`, white `#7A7D8C` / `#A6A9B8` (normal / bright;
normal colours hold 4.5:1 except white, 4.1:1, used for dim text).

**Terminal, Concrete Night** (bg `#0B0C12`, fg `#E6E7EE`, cursor `#8C9BFF`, selection
`#262C5C`): black `#1C1D26` / `#5C5F70`, red `#FF6B6B` / `#FF8F8F`, green `#4CD69B` /
`#7BE6B6`, yellow `#FFD54A` / `#FFE27A`, blue `#6F86FF` / `#96A6FF`, magenta `#FF5FB0` /
`#FF8CC8`, cyan `#4FC3DC` / `#85DCEC`, white `#C9CBD6` / `#FFFFFF` (normal colours hold
6:1 or more; bright black, for comments, 3.1:1).

Sizes: rows 28 (compact) and 32 (comfortable), title bar 28, top bar 40, status bar 24,
sidebar 248 (rail 48), chat dock 400, part tab 3, mat 10 (web) and 8 (phone). Radius 2
everywhere a corner shows. Motion: ease out `cubic-bezier(.16,1,.3,1)`, in-out
`cubic-bezier(.65,0,.35,1)`; 80, 120, 200, 240, 720 ms.

---

## 16. Where it lives

| What | Where |
|---|---|
| The site's tokens (light and dark) and styles | `website/css/tokens.css`, `website/css/site.css` |
| Fonts and their licences | `website/fonts/` |
| The mark's master files and rules | `plans/brand/marks/` (the site's copies in `website/img/`, `website/favicon.svg`) |
| The glass-hall photographs, prompts and reports | `website/art/` (their web sizes in `website/img/`, `make website-images`) |
| The product screenshots and the film still to shoot | `website/shots.todo.md` |
| The share card | `website/og.html` → `website/og.png` |
| The site's structure, copy rules, build and checks | `website/README.md` |
| Measured figures | `hack/demo/measurements.md` |
| The product theme | D184 (`plans/DECISIONS.md`) |

---

## 17. Do and don't

**Do**
- Put one statement in the stair, then tell the story in plain words: the curve, what
  breaks, the idea, xbin working.
- Keep the doubling order in both themes.
- Use cobalt for actions, cyan for focus, signal colours for status.
- Print the trial command first and the install command next to it, each on one line.
- Make Security and Docs quiet, exact and complete.

**Don't**
- Reverse the stair colours in dark mode.
- Use yellow, green or magenta for buttons or links.
- Put type on photographs, or photographs in the product.
- Use field colours for status, or status colours as fields.
- Add warm paper, vermilion, purple-blue gradients, glows, sparkles or robots.
- Call an agent a colleague, or show an agent doing a person's job.
- Say where xbin runs or who runs it.
