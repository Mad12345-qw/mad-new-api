# TraceGuard 风险指挥台设计 QA

## Comparison target

- Source visual truth: `design-prototypes/traceguard/risk-command-center.html`
- Implementation: `server/model-detector/ui/index.html`
- Intended viewport: desktop web app, 1440 × 1024, dark theme, administrator risk-overview state.
- Primary interaction scope: review priority channels, trigger full scan, inspect channel health, open a historical run, access onboarding and settings.

## Evidence status

The source prototype was selected by the user and inspected structurally. The Browser Use URL policy blocks opening `file:///E:/gittok/mad-new-api-release/design-prototypes/traceguard/risk-command-center.html` for a browser-rendered screenshot in this environment. No safe workaround was used.

The implementation has passed static markup, JavaScript syntax, Python compilation, and automated test checks. Those checks do not substitute for a visual comparison.

- Source screenshot: unavailable due to browser URL policy.
- Implementation screenshot: unavailable because the same browser-rendered comparison cannot be completed.
- Density normalization: not applicable; neither comparison image is available.
- Focused-region comparison: blocked for the same reason.

## Required fidelity surfaces

- Fonts and typography: code-level review only; visual wrapping, optical weight, and small-table legibility remain unverified.
- Spacing and layout rhythm: code-level review only; desktop and responsive breakpoints remain unverified in a rendered browser.
- Colors and visual tokens: tokens preserve the selected prototype's dark navy, mint, amber, red, and blue semantic system; visual contrast still requires rendered review.
- Image quality and asset fidelity: the selected risk-command target uses no raster image assets. Existing TraceGuard mark remains unchanged.
- Copy and content: risk wording, failure causes, channel counts, and full-scan actions were reviewed against the selected flow; visual truncation remains unverified.

## Findings

- [P1] Browser-rendered comparison is blocked.
  Location: selected prototype and implementation viewport.
  Evidence: Browser Use rejected the local file URL under its URL policy.
  Impact: layout, text wrapping, responsive behavior, and contrast cannot be accepted as visually verified in this environment.
  Fix: open both local prototype and deployed detector page in an allowed browser surface, capture the same 1440 × 1024 state, then run a visual comparison pass.

## Implementation checklist

1. Confirm risk summary values, channel ordering, and hero actions with live detector data.
2. Capture the selected prototype and deployed implementation at the same desktop viewport.
3. Check the channel table, risk banner, progress panel, history row, and settings panel for P1/P2 visual drift.
4. Update this report to `final result: passed` only after the rendered comparison has no remaining P0/P1/P2 findings.

## Comparison history

- Iteration 1: structural implementation complete; browser visual comparison blocked by local-file URL policy.

final result: blocked
