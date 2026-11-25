---
name: surla-ux-review
description: Run a moderated usability study of Surla. One subagent plays a first-time developer who must build a real product with Surla alone. A second subagent moderates as a UX tester and writes a UX research report with severity-rated findings. Use this whenever someone asks for a UX review, usability test, user test, onboarding or first-run review, or dogfooding of Surla, or wants to know where users get stuck. Not for reviewing UI source code.
---

# Surla UX review

This skill runs a moderated, think-aloud usability test of Surla and turns it
into a UX research report. The product goal is a journey driven by the
participant's existing coding-agent session: after installation and one-time
`surla up`, the participant describes the goal through `/surla` (`$surla`),
uses the browser for human decisions, and reaches an approved merge without
typing terminal commands to move between stages. Commands the coding agent
runs internally are distinct from commands the user must enter. Observe whether the product
actually enables that journey; never supply missing steps as hidden coaching.

The study judges Surla by what its screens show at the moment they show it.
A person who asked for a product watches three things: their coding agent's
pane, the browser page, and, when Herdr is open, the Herdr window. They do
not read Git history or run status commands to learn whether anything is
happening, and they lose patience quickly. So the study keeps a timed record
of those screens from the first second, and the participant waits the way
that person waits: by looking.

That person also knows only what they asked for. A new project starts
blank, and they have never seen its starting files, Surla's worked example,
its generated files, or the frameworks underneath.
So the study saves the words Surla shows them exactly as shown, and judges
each sentence by whether someone who knows only their own request could
understand it and answer it.

Two subagents take part:

- **The participant** plays a developer who has never used Surla. It may read
  only Surla's `README.md` and whatever Surla itself prints or generates, and
  it must reach the goal using Surla alone.
- **The UX tester** moderates. It may read Surla's user documentation but never
  Surla's source code. It sets the challenge, watches each step, asks
  questions, gives hints when the participant is stuck, records bugs and
  usability problems, and writes the report.

You, the agent running this skill, are the session facilitator. You set up the
run, keep the screen record, pass messages between the two subagents, and fix
blocking bugs. You can
read Surla's code, so pass their messages on word for word, and add only the
facts this skill lists. A facilitator who explains or coaches ends up
measuring itself, not Surla. When you are tempted to explain something, write
it in your own notes for the report's limitations section instead.

Use the person's own task and participant profile when they give them.
Otherwise use the defaults:

- **Task:** build an agentic platform that lets end users buy and sell houses.
- **Participant profile:** an impatient, product-minded software developer who
  uses coding agents daily, skims just enough of a README to get moving,
  dislikes boilerplate setup questions or upfront checklists before seeing a
  proposal, is comfortable with Go and the command line, and has never used
  Surla, Goa, or Goa-AI.

## 1. Set up the run

**Where to start.** Run this skill from a folder with no Surla `AGENTS.md` in
it or above it. Some coding-agent harnesses load the working directory's
`AGENTS.md` into every subagent, and Surla's describes its internals. The
skill lives in Surla's `.agents/skills/surla-ux-review/`. To start it from elsewhere, link that
folder into your personal skills folder once:

- `~/.agents/skills/`, read by Codex and Gemini CLI;
- `~/.claude/skills/`, read by Claude Code.

**Folders.** With `<id>` as a timestamp such as `20261101-0900`, create the
participant's folder and, apart from it, the tester's folder:

```text
~/src/surla-ux-<id>-a1/   # one new folder per attempt: -a2, -a3, ...
  surla/       # git clone of github.com/goadesign/surla at the commit under test
  work/        # empty; the participant builds its product here
~/surla-ux-reports/<id>/
  docs/        # README.md, docs/ (with img/), and examples/*/README.md
               # from that commit, copied at their
               # repository-relative paths so the READMEs do not overwrite
               # each other
  observations.md   # the tester's notes, which it writes
  attempt-1/transcript.md   # you append every relayed message here
  attempt-1/screen/         # the screen record: frames and screen-log.md
  report.md    # the final report
```

The participant gets a plain clone, as if it had followed the GitHub link. Do
not install or prebuild Surla for it: working out how to get Surla running is
part of what this study tests. Its brief lists exactly what it may read. That
list, not the folder layout, is what keeps the tester's notes and Surla's
source out of its view.

**Facts to record.** Before starting the subagents, write these at the top of
`attempt-1/transcript.md`:

- the Surla commit, the date, and the OS;
- whether the participant's `work/` folder is empty apart from permitted
  coding-agent settings, and any known pre-existing example destinations.
  Leave existing folders alone and label their ownership unconfirmed;
- the global setup writes this attempt may make. `surla up` and
  `surla setup -global` update the real machine's detected coding-agent MCP
  configuration and skill files, and may install the Herdr mod and add its
  free macOS shortcut in `keys.json`. Record the paths setup reports and
  pre-existing registrations or bindings without recording credentials.
  These files are outside `work/` and may be used by other sessions; do not
  delete or restore them as attempt cleanup;
- the version of Surla installed by the README and the version its starter
  module will download. Record whether they match the commit under test;
- the versions of `go`, `goa`, and `temporal`, or that they are missing;
- which credentials are set, such as `ANTHROPIC_API_KEY`. Record that each is
  set, never its value;
- any personal instruction files your harness loads into every subagent, such
  as a global `CLAUDE.md` or `AGENTS.md`. These can tell the participant
  things the README does not, so the report lists them as a limitation;
- whether commands run inside your coding agent's own sandbox, and whether
  they have network access. Starter initialization downloads modules, and Surla builds
  and runs the code under test as ordinary processes with the user's file
  and network access. Working copies and process groups do not confine that
  code. Keep any coding-agent sandbox restrictions separate from Surla's
  documented execution policy. Give the participant the host access you are
  authorized to give. Trusted Surla operations run as the docs show,
  including initialization from Surla's own unmodified starter templates.
  The product's own code is never compiled, generated from, or run except
  through Surla's documented operations. That includes `goa gen` on its
  design, which runs that design code. If the
  documented workflow needs a step Surla offers no operation for, the study
  reports it as a missing capability. If the access Surla needs is not
  available, that is an environment limitation: the tester marks the
  milestones it blocks as not supported;
- **how the participant reaches Surla's MCP server.** The current README
  uses one-time `surla up` (or `surla setup -global`) to register `surla stdio`
  and the `/surla` (`$surla`) skill. A subagent cannot add an MCP server to
  its own session, so the participant may run an installed coding-agent CLI
  in `work/` and invoke the skill there. Record which clients can run, which
  load the global registration, whether a session reload is needed, and
  whether the environment can show the browser for human decisions. Record
  harness-only CLI launches separately from user workflow steps; a harness
  that cannot preserve the coding session is a study limitation. Do not
  hand it an HTTP route, credential, prewritten MCP configuration or lifecycle
  command. Failure to discover or use the documented integration is evidence;
- whether Herdr and Chorus are installed, whether the Herdr app's window is
  open (`herdr-app status` reports on the open window), whether the agent
  starts inside a live Herdr pane, whether its client has been observed forwarding
  `HC_TERMINAL_ID` and provider settings to stdio, the installed agent
  families and background-worker support, and any project choices or
  dismissed recommendations already present. Do not install a second family just to hide the one-family path;
- **how the study sees and uses the screen** (section 2 depends on these).
  Record the tool that captures each study surface as an image: the tab
  Surla opens in the default browser, and the Herdr window
  (`herdr-app shot`). Record how the study learns which application is in
  front without an image, how the participant clicks on the page, and whether
  anything can click inside the Herdr window. If no tool can see the tab
  Surla opens, record the second browser window the study uses instead
  (section 2). If nothing can click inside the Herdr window, record that
  the companion's buttons cannot be pressed in this run. A run that cannot
  capture the browser page as an image cannot grade the screen: stop and
  say so. Capture those two surfaces and nothing else. An image of the
  whole desktop, or of a browser window with the person's other tabs, saves
  things the study has no business with: take one only when the person
  running the study has said that screen holds nothing but the study, and
  otherwise rely on the name of the application in front. The Herdr window
  can show the person's other spaces, so section 2 limits what is kept of
  its frames. Keep what you do capture in the tester's folder; storing a
  frame privately does not make it right to capture more;
- **which `surla` a program started from the desktop finds.** An app opened
  from the Dock, Finder or a desktop launcher does not read the shell
  startup files that add a Go or personal `bin` folder to `PATH`. A button
  in that app can then fail to run a command that works in every terminal,
  or run an older build. Run these lookups before the attempt and again
  when the participant first reports Surla installed:

  ```sh
  # the interactive shell
  command -v surla
  # a clean login shell
  env -i HOME="$HOME" USER="$USER" SHELL="$SHELL" \
    PATH=/usr/bin:/bin:/usr/sbin:/sbin "$SHELL" -lc 'command -v surla'
  # the bare system path, with no startup files
  env -i PATH=/usr/bin:/bin:/usr/sbin:/sbin /bin/sh -c 'command -v surla'
  ```

  Record each result as "the build under test", "another build" (compare
  `surla version`) or "not found", never the `PATH` value. When the Herdr
  app is running, also record whether the folder holding `surla` is in the
  `PATH` entry of the app's own process, and whether the app was started
  from the desktop or from a terminal. Read that one entry only: filter the
  process's environment down to its `PATH` line inside the command, print
  just yes or no, and never print or save the rest of the environment. An app started from a terminal can
  inherit that terminal's `PATH` and hide the difference; record then that
  the desktop-started path was not exercised. Do not repair a difference
  for the attempt: no symlink, no edited startup file, no relaunch from a
  terminal. Give the tester the results as environment facts;
- **what each mid-build change needs** (see the tester brief's "Mid-build
  changes"), recorded as two separate facts: what this machine provides,
  such as model credentials or the access a live demo needs, and what the
  commit under test provides, such as an operation that regenerates code after a
  design change. The tester uses them to tell "not supported on this
  machine" apart from "blocked by a missing Surla capability".

## 2. Put the session on the screen and record it

A first-time user meets Surla on three surfaces. Set each one up before the
participant sends its first message, and record all of them until the attempt
ends.

**The coding agent's pane.** When the Herdr app's window is open, the
participant's coding-agent session runs in a pane of that window, where a
person could watch it and type. The participant creates it the way a user
starts work on a new project, as a new space (or a new tab) for `work/` with
the agent as its program:

```sh
herdr-app do space.new --name <short name> --folder <participant>/work --argv '["<agent>"]'
```

The command prints the IDs it created, and `herdr-app ls` lists the new
space with its terminal. The participant types into that terminal with
`hc send` and `hc keys`, reads it with `hc read`, and waits on it in short
steps with `hc wait --timeout 60`. These stand in for a keyboard and a pair
of eyes; record them as harness-only steps.

Do not run the participant's agent as a background terminal (`hc run --bg`)
or as a one-shot non-interactive command while the Herdr window is open.
Neither puts a pane on the screen, so Surla has no pane of the user's to
link, and the companion and the page's button that shows the coding agent's
pane go untested.

When no Herdr window is open, the agent runs in an interactive terminal
session the participant can type into and read between turns. Record that,
and mark pane linking, the companion and the show-pane button as not
exercised. Never present such a run as covering them.

**The browser page.** Start looking when the page opens, not when the agent
says it is done. The agent opens the page early and keeps working for
minutes. Those minutes are what a user stares at, and a study that first
looks afterwards never sees them.

- From the participant's first message, record the application in front
  every 2 seconds. The default browser coming to the front, or the agent's
  pane saying it opened the page, is a cue to look and not proof: either
  can happen with no Surla tab.
- Count Surla's tab as open, or open again, only when it is observed: a
  tool shows the tab itself, or the browser confirms a tab at the page's
  address. Ask the browser about that address only, never the link, and
  print just yes or no. Record when the tab was observed and what was in
  front then. If no tab has been observed after two minutes, check every 15
  seconds and say so in the log. If nothing on this machine can observe the
  tab, record each opening as "cue only: tab not observed".
- Use that tab for the rest of the attempt when a tool can both see it and
  click in it.
- Otherwise open a second window on the same page within seconds of
  observing the tab, or of a cue when the tab cannot be observed: run
  `surla --print-link` in `work/` yourself and open its link
  in the browser window the participant uses, without printing the link.
  Record it as a harness-only step. Open it again, the same way, each time
  the screen record shows Surla opening its tab again, and at no
  other time. A second window the product did not open would hide a page the
  user lost.
- The second window shows the same page, but it is not the tab Surla
  opened. Keep recording the application in front. A second window that is
  not on the screen can never be in front, so a click in it cannot show
  whether a button brings another window forward: record window focus as
  not exercised.
- When the participant closes its page, record what was closed: the tab
  Surla opened, the second window, or both. Close Surla's own tab as well
  when a tool can. Count the page as back only when Surla's new tab is
  observed, and record which tab that was. If only the second window could
  be closed, Surla's own tab stayed open throughout and nothing was
  reopened: record closing and reopening the page as not exercised.

**The Herdr window.** `herdr-app status` prints the showing `space` and
`tab`, the `focused_pane`, and the `overlays` and `errors`.
`herdr-app shot <file>` saves the window as a person sees it, including
Surla's companion and any other overlay. The window can be showing one of
the person's other projects, so read the status before taking any image:

- Take and keep the status output at every capture.
- When the status says the study's space is the one showing, take the shot.
  Crop it to the study's panes and Surla's overlays, leaving out the list of
  the person's other spaces and tabs, before anyone views or receives it
  where the tool can crop unseen. Never keep the uncropped file. Read each
  overlay in the crop: an error line in the companion is a finding even
  when the browser page looks fine.
- When the status says another space is showing, take no shot. The status
  already names the space, tab and pane, which is what a focus check needs,
  and it lists the overlays and errors. Capture one of Surla's overlays only
  if a tool can capture that overlay alone; otherwise record that its
  contents could not be inspected.

Give the participant the same rule and the same cropped view.

A shot captures the app even when another window covers it, so it cannot
show whether Herdr is in front; the application in front shows that. Do not
start, stop or restart the person's Herdr app or its core for the study.

**The screen record.** Save frames in `attempt-<N>/screen/`, named by time
and surface, and index them in `attempt-<N>/screen/screen-log.md`. Each row
holds the time, the surface, the file, the page's heading and status line
word for word, Herdr's `space`, `tab`, `focused_pane`, `overlays` and
`errors`, and the application in front (on macOS,
`lsappinfo info -only name "$(lsappinfo front)"`).

| When | Capture |
| --- | --- |
| Before the participant's first message | Herdr's status, and its window when the rule above allows, and the application in front |
| From the first message until Surla's tab appears | The application in front every 2 seconds |
| When the agent's first Surla call returns | Herdr's status, and its window when the rule above allows at about 2 and 10 seconds: is Surla's companion among `overlays`, and what does it show? |
| When the tab appears | The page at about 2, 5 and 10 seconds |
| While an agent or Surla is working: project setup (`init_project`), the stretch between setup and the first proposal (`propose_change`), builds, tests, verification | The page's text every 15 seconds; a frame each time its heading or status changes, and at least one a minute. Herdr's status, and its window when the rule above allows once a minute, and when `overlays` or `errors` change |
| Each proposal, revised proposal or other text the user must read before deciding | The page's complete text, saved word for word as a text file beside the frames, with every section, question, option and example expanded, and a frame of each part |
| Each human decision on the page | The page just before and just after |
| Each click on the page or in the Herdr window | Just before, then about 2 and 10 seconds after: the page, Herdr's status, and its window when the rule above allows, and the application in front. The row just before is the baseline: a click proves a change only against it |

A script may take the captures. Viewing them is your job: look at every kept
frame in the round it was taken. A saved file nobody looked at is not an
observation.

**Flag at once.** Mark the row and tell the tester with the next relay when a
frame shows any of these. State what the frames show and for how long; do
not explain it from the source.

- The page showed work in progress, then returned to an earlier or empty
  state, or asked again for something already given, while work continued.
- A click after which nothing a user can see changed within 10 seconds: the
  same page, the same application in front, the same focused pane.
- Error text on the page, in an overlay, or in the companion.
- Text the user must read, answer or confirm that names something the
  participant never asked for and Surla never explained: the project's
  starting files, Surla's worked example or its features, a file path, a
  command-line flag, a framework term. Quote it from the saved page text.
- No visible change and no explanation for two minutes while an agent is
  working.

## 3. Start the two subagents

Read `references/tester-brief.md` and `references/participant-brief.md` and
fill in every placeholder:

| Placeholder | Value |
| --- | --- |
| `{{TASK}}` | the task |
| `{{PARTICIPANT_PROFILE}}` | the participant profile |
| `{{ENVIRONMENT_FACTS}}` | the recorded facts that matter to a user: installed tools, credentials set, network access and coding-agent sandbox restrictions |
| `{{CODING_AGENTS}}` | the coding-agent CLIs the participant may run, or "none" |
| `{{SCREEN_TOOLS}}` | from section 2: the exact commands the participant uses to start, type into and read its coding agent's pane, to view and click the browser page, and to capture the Herdr window and name the application in front. Say which of these this run lacks |
| `{{PARTICIPANT}}` | the participant's folder |
| `{{TESTER}}` | the tester's folder |
| `{{CHALLENGE}}` | the tester's challenge text, word for word |
| `{{BASELINE_REPORT}}` | the path of an earlier study's `report.md` to compare with, or "none". Give it to the tester only, never the participant |

Start each role as a new subagent that does not inherit this conversation.
This conversation holds the skill, may hold Surla's source or an earlier
attempt, and a participant that sees any of it is no longer a first-time
user. In Claude Code, that means a fresh `general-purpose` agent, not a
`fork`. If your harness can only start subagents that inherit the parent
conversation, stop: report that the participant would be contaminated, and
do not present a run as a first-time experience.

Set each subagent's model explicitly to your
harness's frontier reasoning model, such as Opus in Claude Code or the
high-reasoning model in Codex or Gemini CLI. Both roles need judgment, and a
weaker model turns its own confusion into false usability findings. Both
roles also view the screen record's images. If a role's model cannot view
images, describe each frame for it in `screen-log.md` and list that as a
limitation.

The session runs over many turns, so you need to send each subagent more
messages while it is still running. In Claude Code, start it with the Agent
tool and continue it with SendMessage. If your harness cannot continue a
running subagent, start a fresh one each round instead. Give it its brief,
plus every message it has sent and received so far. The tester also picks up
from `observations.md`. Note the fallback in the limitations, because a
participant restarted every round loses whatever it had not written down.

Start the tester first. It writes its private success criteria to
`observations.md` and replies with the challenge text. Then start the
participant.

## 4. Run the session loop

Each round trip works like this:

1. The participant works until a natural pause and replies with a step
   report. Its brief lists when it pauses, including before one-time setup,
   its first skill invocation, each new human decision on the page, and each
   screen state that would make a person doubt or stop.
2. Append the report to the transcript and send it unchanged to the tester,
   together with the elapsed time since the previous report, the rows added
   to `screen-log.md` since then, and the paths of the frames and page-text
   files they name.
3. The tester replies with up to three questions, an optional hint, an
   optional `CHANGE_REQUEST`, an optional `SCREEN_TASK`, and a decision:
   `continue`, `stop_for_bug`, or `end_session`.
4. If there is a `CHANGE_REQUEST` or a `SCREEN_TASK`, check it before
   relaying it. If it names a Surla command or MCP tool, a checkpoint
   (`CP1`–`CP5`), "Change Card", "checkpoint", the label of a Surla button,
   or a Surla doc or section, send it back to the tester to
   reword and relay nothing yet. A returned draft does not count as handed
   over. If private criteria or tester instructions ever reach the
   participant, treat it as a scope leak.
5. Append the reply to the transcript. On `continue`, send the participant
   only the questions, the hint, the change request and the screen task,
   word for word.

**While the participant waits.** Keep capturing on section 2's schedule
whether or not a report is due. The participant's brief limits it to the
screen while its agent or Surla is working. From its actual tool calls, count
each off-screen check: a command or file read used to learn what is
happening, such as `git log`, `git status`, listing or reading project
files, or `surla status`. Tell the tester each one, with the screen state it
followed. An off-screen check does not end the attempt. It shows that the
screen left a question open, and the tester grades the screen as it stood
before the check.

**Bounds.** Unless the person running the skill sets others: 4 hours of
wall-clock time per session, and 3 attempts. If a running participant sends
no report for 30 minutes, tell the tester "no report within 30 minutes"; it
decides `continue` or `end_session`. A second missed report ends the attempt.
When a bound ends an attempt or the session, tell the tester which one, so it
can close every open change with that reason.

A level-2 hint names a doc section that the participant cannot otherwise
open. Copy that one document into `work/` and say where it is. Record the
copy as part of the hint.

**Check for scope leaks** after every few rounds. The participant's reports
are its own account, so compare them with its actual tool calls wherever your
harness keeps a subagent transcript. Also read the logs its coding agent saved
in `work/agent-logs/`. A leak is reading anything outside the participant's
allowed list, loading this skill, building the product without Surla, or
compiling, generating from, or running the product's code except through
Surla's documented operations.

A participant cannot unlearn what it read, so a leak ends the attempt. Tell
the tester what was read and when. The tester records the leak as a finding,
because reaching for the source usually means the docs failed, and marks
later milestones in that attempt as not valid. Then restart, as in section
5 step 4, with the same build. If nothing can be checked, note in the
limitations that leaks were only self-reported.

**When an attempt or the session ends**, do two things:

- Stop the long-running processes the participant reported, such as `surla
  serve` or a Temporal dev server. Check first that each PID still runs the
  command the participant started in this attempt, for example that
  `ps -p <pid> -o command=` shows the reported binary and folder. PIDs get
  reused. Leave running any process you cannot match. The participant's
  coding agent in its Herdr pane is one of them: end its program with
  `hc kill` after checking the terminal is the one this attempt created.
  Leave the Herdr space in place and record its name; never close a space
  the attempt did not create.
- Delete nothing else. Every project folder the participant created is
  evidence for the report, including a project initialized outside `work/`,
  and may hold a Git repository with unpushed work. Keep machine-wide setup
  changes recorded above; they are not disposable attempt files. Each attempt
  gets a new folder, so nothing needs to be cleared for the next one.

In the next transcript, record the exact path of every folder outside `work/`
that the participant reported or that appears in `work/agent-logs/`, and
every process left running. Label them "ownership unconfirmed". Tell the
tester when the next attempt starts, so it does not log a leftover, such as
initialization refusing a nonempty folder or a port already in use, as a
Surla bug. If someone later asks for cleanup, inspect each exact target
first: nested repositories, `git status`, unpushed commits, and whether
deleting it is authorized.

## 5. Handle a blocking bug

When the tester decides `stop_for_bug`, it names the bug and gives the steps
to reproduce it. A blocking bug is one where Surla crashes, behaves
differently from its documentation, or lacks an MCP/browser operation its
documented workflow needs, and no documented workaround exists. A terminal
workaround can unblock the product task while still failing the zero-command
journey goal; record both outcomes rather than silently counting success.

1. Reproduce the bug yourself with the trusted Surla CLI, using the host and
   network access you are authorized to use, so a restriction in your coding
   agent is not mistaken for a Surla bug. The product's code is still
   compiled, generated from, and run only through Surla's documented operations.
   - If the failure comes from the harness, tell the tester "environment
     limitation, not a Surla bug" and which milestones it affects.
   - A missing capability, meaning no documented Surla operation for a step the
     documented workflow needs, counts as a real bug even when Surla lacks
     it by design. Go to step 2.
   - If Surla behaves as designed, tell the tester "reproduced; behavior is as
     designed", with no code details. The tester records a usability finding,
     may give a hint, and the session continues.
2. If it is a real bug, fix it following the repository's `AGENTS.md`: a
   separate clone and branch, the design gate, tests, an independent review,
   and a pull request. Fix the cause, not the symptom the participant
   happened to hit.
3. If the fix is in code a starter downloads, publish a version containing
   it before restarting. Confirm that the documented installation and starter
   initialization resolve that version, rather than an earlier dependency.
   An exact Go pseudo-version is printed by
   `go list -m github.com/goadesign/surla@<sha>`; update a documented version
   pin when that is what selects the dependency. Do not patch the participant's
   project or supply an undocumented override to make the attempt pass.
4. Restart from scratch:
   1. Stop processes and record leftovers as in section 4.
   2. Create a new participant folder, `~/src/surla-ux-<id>-a<N+1>/`, holding
      a clone at the newest commit of the fix, including any
      version-pin follow-up, and an empty `work/`. Leave the previous
      attempt's folder in place as evidence.
   3. Copy the user docs from that commit into the tester's `docs/` again, so
      the tester judges the participant against the docs it is reading.
   4. Start `attempt-<N+1>/transcript.md` with the newly recorded facts.
   5. Start a **fresh** participant, with its brief filled in again for the
      new folder. One that remembers the failed attempt would skip the steps
      that confused it.
   6. Keep the same tester. Tell it that a new attempt has begun on that
      commit and which docs changed. Its earlier findings carry into the
      report.

Fix and restart after every blocking bug. If the person invoking the skill
set a limit on attempts, stop and report when it is reached.

## 6. Debrief and report

Keep these outcomes separate: product built, verified, approved, and merged;
one-time installation/setup; later user-entered terminal commands; commands
the coding agent runs internally; harness-only invocations; and steps
completed with help. For each applicable path, record observed success or why it was not
exercised: live guide adoption, page recovery through the guide or companion,
inline agent choices, builder-only builds with configured tests, one-family
operation, missing Herdr, persisted choices/notices after reload, human
Merge with refusal recovery, what the page showed from its first seconds to
the first proposal, the page's button that shows the coding agent's pane,
the companion's overlay and its buttons, and commands the Herdr app runs
when it was started from the desktop. Do not add fixtures or hints to force
coverage.
A source claim, successful command or assignment is not proof of a visible
outcome. The report's severity ratings and transcript evidence remain the
moderator's responsibility.

When the tester decides `end_session`, it sends the participant its debrief
questions. Relay the questions and the answers. Then send the tester the text
of `references/report-template.md` and ask it to write `report.md`.

Before handing the report over, check it against the transcripts:

- Every finding points to a step that happened.
- Quotes are exact. A quote of page text matches the saved page text; a
  finding that rests on a paraphrase of it goes back to the tester.
- Every finding about a screen names its frames and their times, and the
  frames show what the finding says.
- The counts of hints, errors, and milestones match the transcripts.
- Every change request in the report was relayed word for word, as the
  transcript shows, and every planned change has exactly one outcome per
  attempt.

Add each bug fixed during the study, with its PR link. Correct mismatches
with the tester rather than rewriting its conclusions yourself. Then give the
person who invoked the skill:

- the path to `report.md`;
- the top three findings, in plain words;
- the bugs fixed, with PR links;
- what could not be tested on this machine.
