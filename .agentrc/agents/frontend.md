# Frontend Context — Cerberus (TUI)

> Project-specific TUI conventions. Loaded by the frontend agent role when working in this project.
> Lives at `cerberus/.agentrc/frontend.md`.

## Stack

- **Framework:** Bubbletea v1.3.10 (Elm-architecture TUI)
- **Styling:** Lipgloss v1.1.0 (terminal styling/layout)
- **Components:** Bubbles v1.0.0 (standard TUI components — not heavily used yet)
- **CLI:** Cobra v1.10.2 (command routing, TUI is the default command)
- **Language:** Go 1.25.3

## Project Structure

```
internal/tui/
├── model.go       # Root Model struct, Init, Update (key handling, tick loop)
├── view.go        # Main View rendering (service table, summary bar, help footer, scrollbar)
├── styles.go      # Shared Lipgloss color constants and style definitions
├── logview.go     # LogViewModel sub-model (log file viewer with scroll/follow)
├── grouping.go    # ServiceGroup type, GroupByProject, tag collection/filtering, flatItem cursor list
└── groupview.go   # Group header rendering, tag filter indicator
```

The TUI is launched from `cmd/cerberus/main.go` via `runTUI()` (the default Cobra command). It receives `[]*service.ManagedService` from the service layer.

## Component Inventory

| Component | Location | Use for |
|-----------|----------|---------|
| `Model` | `tui/model.go` | Root Bubbletea model — owns service list, cursor, sort/filter/group state |
| `LogViewModel` | `tui/logview.go` | Sub-model for per-service log file viewer with scroll and auto-follow |
| `ServiceGroup` | `tui/grouping.go` | Groups services by project name for the grouped view |
| `flatItem` | `tui/grouping.go` | Flattened cursor-addressable list mixing group headers and service rows |
| Styles | `tui/styles.go` | Shared color palette and Lipgloss style vars |
| Group view | `tui/groupview.go` | Renders group header rows and tag filter indicator |

## Patterns to Follow

### Model Composition
- The root `Model` delegates to `LogViewModel` when a log view is active. This is done by checking `m.logView != nil` in both `Update` and `View`.
- Sub-models return a custom exit message (`logViewExitMsg`) to hand control back to the parent.
- This delegation pattern should be followed for any future sub-views (e.g., detail panel, config editor).

### Message Flow
- `tickMsg` fires every 2 seconds via `tea.Tick` — polls all services and checks for completed builds.
- `tea.WindowSizeMsg` updates `width`/`height` on both the root model and any active sub-model.
- `tea.KeyMsg` is handled in a single switch in `Update`. Filter mode intercepts keys first.
- Custom messages (e.g., `logViewExitMsg`) are checked before the main switch.

### Cursor and Scrolling
- Grouped mode uses a `flatItems` slice mixing headers and service rows. The cursor indexes into this flat list.
- Viewport scrolling is managed via `scrollOff` and `clampedScrollOff()` — keeps cursor visible within terminal height.
- `maxServiceRows()` calculates available rows by subtracting fixed chrome lines from terminal height.

### Rendering
- `cell(width, text)` renders fixed-width columns via Lipgloss to maintain alignment with ANSI escapes.
- Column widths are defined as constants (`colWidthName`, `colWidthStatus`, etc.) at the top of `view.go`.
- The entire view is horizontally centered by padding each line with spaces based on terminal width.
- A scrollbar is rendered alongside rows when content overflows (`buildScrollbar`).

### Key Bindings
| Key | Action | Scope |
|-----|--------|-------|
| `j/k` | Move cursor | Main + Log |
| `s` | Start service | Main |
| `x` | Stop service | Main |
| `r` | Restart service | Main |
| `b` | Build service | Main |
| `R` | Rebuild (build + restart) | Main |
| `enter` | Toggle group collapse / open URL | Main |
| `l` | Open log viewer | Main |
| `g` | Toggle grouped/flat view | Main |
| `t` | Cycle tag filter | Main |
| `tab` | Cycle sort field | Main |
| `/` | Enter filter mode | Main |
| `a` | Start all | Main |
| `X` | Stop all | Main |
| `G` | Scroll to bottom | Log |
| `g` | Scroll to top | Log |
| `q/esc` | Quit / exit sub-view | Both |

### Styling Convention
- All colors defined as `lipgloss.Color` vars in `styles.go` with semantic names (`colorGreen`, `colorAccent`, etc.).
- Status-specific styles (`runningStyle`, `stoppedStyle`, etc.) compose these colors.
- New styles should be added to `styles.go`, not inline in view functions.

## Anti-Patterns Found

1. **Duplicated style definitions** — `logview.go:18-60` re-declares color constants and styles that overlap with `styles.go` (same hex values for accent, dim, green, etc.). These should reference the shared vars from `styles.go`.

2. **Blocking sleep in Update** — `model.go:213` and `model.go:229` call `time.Sleep(500ms)` inside the `Update` handler (and a goroutine spawned from it). The main-thread sleep on line 213 blocks the entire TUI for 500ms during restart. Use a `tea.Cmd` with `tea.Tick` or a channel-based message instead.

3. **Redundant min/max helpers** — `view.go:408` defines `min()` and `logview.go:287` defines `maxInt()`. Go 1.21+ provides built-in `min()` and `max()`. Remove these and use the builtins.

4. **Bubble sort in sorted()** — `model.go:403-419` uses a nested-loop bubble sort. Use `sort.Slice` from the standard library for clarity and consistency (already used in `grouping.go:38`).

5. **Goroutine fire-and-forget in rebuild** — `model.go:237-248` spawns a goroutine for build+restart with no way to report errors back to the TUI via messages. Errors are silently stored on `svc.BuildErr` and only visible on the next tick poll. Use a `tea.Cmd` that returns a build-complete message.

6. **OS-specific command** — `model.go:265` uses `exec.Command("open", ...)` which only works on macOS. Should use `runtime.GOOS` to select the right opener (`xdg-open` on Linux, `start` on Windows).

## Reference Implementations

| Pattern | Reference File | Why it's good |
|---------|---------------|---------------|
| Sub-model delegation | `tui/logview.go` | Clean Elm-architecture sub-model with own Update/View, exit message, scroll state |
| Grouped cursor list | `tui/grouping.go` | Well-structured flat-item abstraction over hierarchical data, clean separation of grouping/filtering logic |
| Style definitions | `tui/styles.go` | Centralized color palette and semantic style vars — compact and scannable |

## Design Tokens

| Token | Hex | Use |
|-------|-----|-----|
| `colorGreen` | `#00d787` | Running/healthy status |
| `colorRed` | `#ff5f87` | Stopped/failed/unhealthy status |
| `colorYellow` | `#ffd75f` | Starting/building status, filter indicator |
| `colorBlue` | `#5fafff` | Column headers, action hints, auto-restart flag |
| `colorAccent` | `#7b68ee` | Title bar, cursor, help keys, group headers |
| `colorDim` | `#666666` | Inactive text, separators, help descriptions |
| `colorWhite` | `#ffffff` | Title text |
| `colorBg` | `#1a1a2e` | Background (defined but not applied globally) |
| Selected row bg | `#2a2a4e` | Highlighted row background |

## Notes

- The TUI is the default command (`cerberus` with no args). All other commands (`up`, `down`, `status`, etc.) are headless CLI subcommands.
- The service layer (`internal/service/`) is the data source — the TUI only reads from `ManagedService` structs and calls `Start()`, `Stop()`, `Build()` methods.
- `colorBg` is defined in styles but never applied as a global background — the TUI relies on the terminal's native background.
- Total TUI code: 1,369 lines across 6 files.
