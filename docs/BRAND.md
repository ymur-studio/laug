# laug brand kit

What code, tests and the judge read of laug's look: the eight color tokens, the type, the mark's state sign and the
rules of each surface laug draws. `cmd/laug/colour.go`, the renderer under `_report` and `docs/brand/tokens.css` take their colors
from the Colour table, and the judge (`internal/judge`, run as `bin/laug judge -write`) writes verdict.md as Surfaces
says.

## The mark's state

The wisp is the one sign that carries state. In a live view, and only beside the verdict word, it takes moss for
`ADVANCE` and ember for `BLOCK`; the hum and the bowl keep the ink. The still mark never does: not in the README, a
favicon, the social card or print. The files are `laug-mark-advance-light.svg`, `laug-mark-advance-dark.svg`,
`laug-mark-block-light.svg` and `laug-mark-block-dark.svg`. In a page, give the mark `class="laug-mark"` and
`data-verdict="advance"` or `"block"`, and the wisp `class="sign"`; `tokens.css` colors it.

One value each, on light and on dark, because the Colour table names one moss and one ember. The wisp in moss is
2.95:1 on steam and 5.4:1 on basalt; in ember, 4.3:1 on steam and 3.8:1 on basalt, computed as under Colour. Moss on
steam is under WCAG's 3:1 for graphics, so the wisp is never the only sign: the verdict word beside it carries the
meaning. <!-- source: WCAG 2.1, 1.4.11 -->

## Colour

Eight tokens, in `tokens.css`. Water is the one accent; everything else is
neutral or a verdict.

| Token | Hex | Named for | Use |
| --- | --- | --- | --- |
| basalt | `#10181C` | lava rock | dark surface; ink on light |
| deep | `#163A44` | deep water | text and links on light; raised cards on dark |
| water | `#5FB3BF` | the pool | the accent: strokes, series lines, links on dark |
| silica | `#CFE3E4` | mineral edge | rules, borders, secondary text on dark |
| steam | `#EEF3F2` | steam | light surface; ink on dark |
| warm | `#D99A3A` | warmth | a warm cell, a lease in progress, annotations |
| moss | `#5B9B6A` | | ADVANCE |
| ember | `#C44B32` | | BLOCK |

Contrast, computed from the hex values with WCAG 2's contrast ratio: basalt on
steam 16.0:1, deep on steam 10.9:1, water on basalt 7.4:1, silica on basalt
13.5:1. Water on steam is 2.2:1, so on light surfaces water is a fill and stroke
color and deep carries the text. Steam on moss is 2.95:1 and steam on ember
4.3:1: the verdict colors are for badges, threshold lines and panel
backgrounds, never for body text.

Dark theme is the default voice of the project (terminals, Grafana, the social
card). Light is for documents.

## Type

- Prose and UI: Inter, falling back to the system sans. GitHub renders the
  README in its own type, which is fine; the kit only governs surfaces laug
  draws itself.
- Code, commands, numbers in tables: a monospace with tabular figures
  (Source Code Pro on the social card, JetBrains Mono, or the system mono).
  Every number the README quotes is a measurement, so it is set in mono.
- The wordmark: Literata 500.
- Headings are sentence case. No title case anywhere.

## Surfaces

| Surface | Rule |
| --- | --- |
| README header | the wordmark via `<picture>`, the bold description, two verdict lines, then the etymology; nothing above |
| Repo description | the one-line description, verbatim |
| Social preview | `laug-social.png`, 1280 by 640, uploaded in repo settings; the card is described below |
| Favicon | `favicon.svg`, `favicon.ico`, `apple-touch-icon.png`; one color |
| Terminal output | the prefix `laug ·` on a command's first line, then plain text; verdicts by the rule below |
| GitHub job summary | test.yml's verdict is the fake gate's verdict.md as the judge wrote it: the heading `laug · verdict for raun <commit>`, marked `(GATE_FAKE=1: fixtures, no cell ran)`, the gate line, the item table, R7's comparison table when there is one, the rule table, the host line, a BLOCK's class, then `**ADVANCE**` or `**BLOCK**` alone on the last line. Under it, `laug · test.yml on a fresh clone`: a row a step with its seconds and exit, then `bin/laug gate rejudge -all`'s counts line. The verdict heading is the judge's historical form: it calls a gate a raun. The judge writes it until its next version |
| Grafana | folder `laug`; board `laug · gate` in each cell, and five in the observability stack (`_obs/stack/`): `laug · gate run`, `laug · raun`, `laug · cell`, `laug · gates` and `laug · stack`; series in water, thresholds in ember, its error-rate stat water under the max and ember at or over it, with no verdict word; in the stack's boards also the A/A band as a fill in deep, marks and windows in warm, the incumbent in silica; timezone UTC, dark theme; a color always comes with its words in the panel, never alone |
| Design doc | title `laug: ephemeral cells with a warm pool for release validation`; a lead paragraph naming its reader and its hosts; twelve sections |
| Commit message | `type(scope): what changed`, lowercase, imperative, under 60 characters; body carries the number that moved |
| Release notes | two lines: what changed, the one number that moved |

The README's verdict lines and the social card's give each verdict the number that earned it. The card
(`laug-social.svg`): the lockup in steam on basalt, the one-line description,
`ADVANCE  30 of 30 gates of a release against itself` in moss and `BLOCK  3 of 3 gates, 25 µs delay into Postgres` in
ember, and the verbs `laug lease · laug raun · laug gate run` in water.

Both lines are the incumbent arm's validation on the lab host, an m8i.8xlarge (32 vCPU, 128 GiB), as CAPABILITIES'
Noise and Controls rows give it. 30 of 30 A/A gates ADVANCEd with the tolerances judging
(`gate/sets/validation-arm-3item-m8i.8xlarge.txt`). The known-slow control delayed every packet into the release
cell's Postgres by 25 µs and BLOCKed 3 of 3, class regression on R7 alone, R1 to R6 passing
(`gate/sets/control-arm-known-slow.txt`; `docs/tables/gates.jsonl#g20261018-163914`, `#g20261018-170110`,
`#g20261018-172301`). Boot 4 adds to neither count, so both stand (`cells/results/2026-10-24-boot4.txt`, The 19 gates
together).

The terminal color rule: a verdict is written in capitals on its own line. `ADVANCE` is bold in moss and `BLOCK`
bold in ember only when the output is a terminal (TERM set and not dumb); piped, under `-json` or with `NO_COLOR`
set, the word is plain. <!-- source: no-color.org --> `cmd/laug/colour.go` does this, and a test ties its escapes
to the Colour table above.
