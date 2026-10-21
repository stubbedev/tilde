# AGENTS.md

Read `README.md` for what tilde is, and `docs/architecture.md` for the
decisions every change builds on.

## Gates

Run before every commit:

```sh
nix develop -c just gates
nix build -L
```

After every push, watch CI until it is green (`gh run watch`). A red `main`
is fixed before anything else lands.

The dev shell runs the GPU tests on Mesa's llvmpipe. On a machine with a GPU,
run `just hardware` after changes to rendering, DRM, dmabuf, capture or frame
pacing: it runs the GPU, DRM-without-master and GLES harness tests on the
host's own EGL driver, then measures es2gears against headless GLES tilde in a
private runtime dir (Xwayland off) and fails below `min_fps` (57 by default,
for the 60 Hz headless output).

## The tracker

GitHub is the only tracker. Nothing is planned in local files or chat.

| Thing | Where | Role |
| --- | --- | --- |
| Issues | `stubbedev/tilde` | One unit of work each; the body is the spec |
| Milestones | `M0 Foundation` … `M9 Multi-GPU` | Phases; each description holds the exit criteria |
| Project | [tilde board](https://github.com/users/stubbedev/projects/3) | Status of every issue: Todo, In Progress, Done |

Board automation, as configured:

- An issue added to the board starts as **Todo**.
- Closing an issue moves it to **Done**, and moving an item to Done closes its issue.
- Nothing adds new issues to the board automatically: always create them with `--project tilde`.
- Nothing sets **In Progress**: run `just start <issue>`.

Views: **Roadmap by milestone** (table grouped by milestone) and **Board**
(Status columns, a row per milestone). The API cannot update a view. To
change one, create a new view with
`POST /users/stubbedev/projectsV2/3/views` (the path takes the login, not the
numeric user ID), passing `group_by` and `vertical_group_by` as field IDs.

## Picking work

1. Take the lowest open milestone, then `prio:critical` before `prio:high`
   before `prio:normal`.
2. Only take an issue whose **Depends on** issues are all closed. Work from a
   later milestone is fine when it is unblocked and pure; the WM core (#21)
   does not need the protocol stack, for example.
3. Read the whole issue, including its **Reference** section, before writing code.
4. `just start <issue>`.

```sh
gh issue list -R stubbedev/tilde --milestone "M0 Foundation" --state open \
  --json number,title,labels --jq '.[] | "#\(.number) \(.title) [\([.labels[].name] | join(", "))]"'
```

## Landing work

- Commit straight to `main`. Subject: `area: change (#N)`, where `area` is the
  package or the issue's `area:` label. The body says why; the source carries
  no comments.
- One logical change per commit. An issue may take several commits, each
  referencing it.
- Push, wait for green CI, then close with the commit:
  `gh issue close N -c "Landed in <sha>: <what landed>"`.
- If only part of an issue landed, leave it open, comment what landed and what
  remains, or split the remainder into a new issue and link it.
- The issue body stays the truth. When implementation changes the scope or
  the plan, edit the body and say why in a comment.

## New issues

Every bug or gap found while working is filed and fixed, never just
mentioned. An unrelated find gets its own issue and its own commit; then
return to the task in progress.

```sh
gh issue create -R stubbedev/tilde --project tilde \
  --milestone "M1 Core protocols" --label "area:protocol,prio:high" \
  --title "protocol: <what>" --body-file - <<'MD'
## Goal
## Scope
## Done when
## Depends on
## Reference
MD
```

- **Title**: `area: what`, the same form as commit subjects.
- **Body**: Goal, Scope, Done when, Depends on (`#N` per blocker), and
  Reference when a reference implementation exists. Leave out empty sections.
- **Labels**: at least one `area:*`, exactly one `prio:*`, `bug` for defects,
  `decision` when it settles a design choice.
- **Milestone**: the earliest milestone whose exit criteria need it.
- **Dependencies**: when a new issue blocks an existing one, add it to that
  issue's **Depends on**.

## Decisions

Issues labelled `decision` are settled before the code that depends on them.
Comment the outcome and the reasons on the issue, record it in
`docs/architecture.md`, then implement. Ask the user first when the choice is
costly to reverse.

## Milestones

- Close a milestone only when all its issues are closed **and** its exit
  criteria have been exercised. Say how they were verified in the closing
  comment of its last issue, then
  `gh api -X PATCH repos/stubbedev/tilde/milestones/<number> -f state=closed`.
- If exit criteria change, edit the milestone description.
- To move an issue to another milestone, comment why: usually a dependency
  that was missed.

## References

Issues link Hyprland, aquamarine, hyprwayland-scanner and hy3 at pinned
commits. When refreshing a link, pin the new commit too. What each licence
allows is in `docs/architecture.md`; never port hy3 code.
