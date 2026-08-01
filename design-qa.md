# Design QA

## Source of visual truth

- Reference: `/var/folders/lp/vhgkn01n4hl0mpwnz4541mvh0000gn/T/codex-clipboard-1de926ba-8f3e-461b-88a5-cd33358bb652.png`
- Implementation: `/tmp/vfs-link-cancel-dock-implementation.png`
- Viewport: 1440 × 785 CSS pixels. The supplied reference is 2880 × 1570 physical pixels at the equivalent 2× scale.
- State: desktop file table with one throttled background upload and the process Dock expanded.

## Comparison history

### Pass 1 — full screen and lower table/Dock region

- Compared the reference and implementation together at the same logical viewport.
- The table wrapper now has `padding-bottom: 0px`; its footer remains at the table edge while the fixed Dock overlays the lower content.
- The Dock remains bottom-centered with the existing border, radius, surface, shadow, and progress treatment.
- The expanded Dock adds visible `Cancel all`, per-file `Cancel`, and collapse controls without changing the surrounding page layout.
- The implementation fixture contains two files rather than the six production folders in the reference; this data-density difference does not affect the layout under review.

### Responsive and interaction checks

- At 390 × 844, both collapsed and expanded Dock states had `scrollWidth === clientWidth === 390`.
- Collapsing retained the aggregate byte progress and percentage. Adding another upload preserved the user's collapsed preference.
- A new failed upload automatically expanded the Dock so Retry and Dismiss were visible.
- Canceling one active upload and confirming Cancel all removed pending rows immediately; the local API contained no canceled files, objects, or upload-session records afterward.

Final result: passed
