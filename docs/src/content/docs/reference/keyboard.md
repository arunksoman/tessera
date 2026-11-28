---
title: Keyboard shortcuts
description: Every key in the web app and the terminal UI, side by side.
sidebar:
  order: 2
---

The web app and the TUI share one key map where they can. Use `Cmd` for `Ctrl` on macOS in the web app.

## Shared

| Action | Web app | TUI |
|---|---|---|
| Run | `Ctrl+Enter` or `F5`: the script, or the selection | `Ctrl+Enter` or `F5`: the statement at the cursor |
| Run the statement at the cursor | `Ctrl+Shift+Enter` or `Shift+F5` | `F5` |
| Run the whole script | `Ctrl+Enter` | `F6` |
| Cancel a running statement | `Esc` | `Esc` |
| Complete | `Ctrl+Space` | `Tab` or `Ctrl+Space` |
| New tab | `Alt+T` | `Alt+T` |
| Close tab | `Alt+W` | `Alt+W` |
| Next, previous tab | `Alt+]`, `Alt+[` | `Alt+]`, `Alt+[` |

## Web app only

| Action | Keys |
|---|---|
| Format SQL | `Ctrl+Shift+F` |
| Find and replace | `Ctrl+F` |
| Undo | `Ctrl+Z` |
| Edit the active cell | `Enter` or double-click |
| Copy the active cell | `Ctrl+C` |
| Open a context menu | Right-click, the context menu key, or `Shift+F10` |
| Close a menu or dialog | `Esc` |

## TUI only

| Action | Keys |
|---|---|
| Command palette | `Ctrl+K` |
| Help | `F1` |
| Explain, as text or JSON | `F7`, `F8` |
| Statement history | `Ctrl+R` |
| Apply pending edits | `Ctrl+S` |
| Switch connection | `Alt+C` |
| Focus tree, editor, grid | `Alt+1`, `Alt+2`, `Alt+3` |
| Focus next pane | `Shift+Tab` |
| Theme: system, light, dark | `F9` |
| Quit | `Ctrl+Q` |

| Pane | Keys |
|---|---|
| Tree | Arrows or `h` `j` `k` `l` move, `Enter` opens, `d` describes, `r` refreshes |
| Grid | Arrows or `h` `j` `k` `l` move, `PgUp`/`PgDn`, `g` and `G` jump, `Enter` edits or shows a cell, `v` shows it |
| Grid | `s` sorts, `f` or `/` filters, `c` clears filters, `u` undoes an edit, `[` and `]` change result |
| Edit box | `Enter` saves, `Alt+Enter` adds a line, `Ctrl+N` sets NULL, `Esc` cancels |
