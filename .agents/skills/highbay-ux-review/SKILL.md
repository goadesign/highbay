---
name: highbay-ux-review
description: Run a moderated usability study of Highbay. One subagent plays a first-time developer who must build a real product with Highbay alone. A second subagent moderates as a UX tester and writes a UX research report with severity-rated findings. Use this whenever someone asks for a UX review, usability test, user test, onboarding or first-run review, or dogfooding of Highbay, or wants to know where users get stuck. Not for reviewing UI source code.
---

# Highbay UX review

This skill runs a moderated, think-aloud usability test of Highbay and turns
it into a UX research report. After installation and one-time `highbay up`,
the participant runs `highbay`, states a product goal in the browser, and works
through an approved merge. If the task starts in an existing coding-agent
session, the participant can invoke `/highbay` (`$highbay` in Codex) to attach
it, then work in the same browser workspace. Observe whether the product
enables the journey. Record commands run by the participant, the coding
agents, and the study harness separately.

Judge the browser workspace by what it shows while the participant is
working. Include **Conversation** and each **Terminal** the participant opens.
If studying guide attachment, also record the original coding session
where the participant sees it. Observe how long the participant waits and
what signals they can see before providing any help.

That person also knows only what they asked for. A new project starts
blank, and they have never seen its starting files, Highbay's worked example,
its generated files, or the frameworks underneath.
So the study saves the words Highbay shows them exactly as shown, and judges
each sentence by whether someone who knows only their own request could
understand it and answer it.

Two subagents take part:

- **The participant** plays a developer who has never used Highbay. It may read
  only Highbay's `README.md` and whatever Highbay itself prints or generates, and
  it must reach the goal using Highbay alone.
- **The UX tester** moderates. It may read Highbay's user documentation but never
  Highbay's source code. It sets the challenge, watches each step, asks
  questions, gives hints when the participant is stuck, records bugs and
  usability problems, and writes the report.

You, the agent running this skill, are the session facilitator. You set up the
run, keep the screen record, pass messages between the two subagents, and fix
blocking bugs. You can
read Highbay's code, so pass their messages on word for word, and add only the
facts this skill lists. A facilitator who explains or coaches ends up
measuring itself, not Highbay. When you are tempted to explain something, write
it in your own notes for the report's limitations section instead.

Use the person's own task and participant profile when they give them.
Otherwise use the defaults:

- **Task:** build an agentic platform that lets end users buy and sell houses.
- **Participant profile:** an impatient, product-minded software developer who
  uses coding agents daily, skims just enough of a README to get moving,
  dislikes boilerplate setup questions or upfront checklists before seeing a
  proposal, is comfortable with Go and the command line, and has never used
  Highbay, Goa, or Goa-AI.

## 1. Set up the run

**Where to start.** Run this skill from a folder with no Highbay `AGENTS.md` in
it or above it. Some coding-agent harnesses load the working directory's
`AGENTS.md` into every subagent, and Highbay's describes its internals. The
skill lives in Highbay's `.agents/skills/highbay-ux-review/`. To start it from elsewhere, link that
folder into your personal skills folder once:

- `~/.agents/skills/`, read by Codex and Gemini CLI;
- `~/.claude/skills/`, read by Claude Code.

**Folders.** With `<id>` as a timestamp such as `20261101-0900`, create the
participant's folder and, apart from it, the tester's folder:

```text
~/src/highbay-ux-<id>-a1/   # one new folder per attempt: -a2, -a3, ...
  highbay/       # git clone of github.com/goadesign/highbay at the commit under test
  work/        # empty; the participant builds its product here
~/highbay-ux-reports/<id>/
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
not install or prebuild Highbay for it: working out how to get Highbay running is
part of what this study tests. Its brief lists exactly what it may read. That
list, not the folder layout, is what keeps the tester's notes and Highbay's
source out of its view.

**Facts to record.** Before starting the subagents, write these at the top of
`attempt-1/transcript.md`:

- the Highbay commit, the date, and the OS;
- whether the participant's `work/` folder is empty apart from permitted
  coding-agent settings, and any known pre-existing example destinations.
  Leave existing folders alone and label their ownership unconfirmed;
- the global setup writes this attempt may make. `highbay up` may install
  dependency executables and updates detected coding-agent MCP registrations
  and skill files. Record the reported paths and any previous registrations
  without credentials. These paths can be used by other sessions. Keep
  them outside attempt cleanup;
- the version of Highbay installed by the README and the version its starter
  module will download. Record whether they match the commit under test;
- the versions of `go`, `goa`, and `temporal`, or that they are missing;
- which credentials are set, such as `ANTHROPIC_API_KEY`. Record that each is
  set, never its value;
- any personal instruction files your harness loads into every subagent, such
  as a global `CLAUDE.md` or `AGENTS.md`. These can tell the participant
  things the README does not, so the report lists them as a limitation;
- whether commands run inside your coding agent's own sandbox, and whether
  they have network access. Starter initialization downloads modules, and Highbay builds
  and runs the code under test as ordinary processes with the user's file
  and network access. Working copies and process groups do not confine that
  code. Keep any coding-agent sandbox restrictions separate from Highbay's
  documented execution policy. Give the participant the host access you are
  authorized to give. Trusted Highbay operations run as the docs show,
  including initialization from Highbay's own unmodified starter templates.
  The product's own code is never compiled, generated from, or run except
  through Highbay's documented operations. That includes `goa gen` on its
  design, which runs that design code. If the
  documented workflow needs a step Highbay offers no operation for, the study
  reports it as a missing capability. If the access Highbay needs is not
  available, that is an environment limitation: the tester marks the
  milestones it blocks as not supported;
- **how the participant opens the workspace.** The README uses one-time
  `highbay up` to register Highbay with coding agents, then `highbay` in the
  project folder to open the browser. Record the available coding-agent
  clients, installed Herdr Core and Chorus versions, and whether global
  registration is already present. If testing existing-agent attachment,
  record which client can load the integration and whether the study can
  observe its original session. Use documented entry points and record
  any harness-only launches separately;
- **how the study sees and uses the screen.** Record the tools for viewing,
  clicking, typing, and capturing the Highbay tab, including Conversation
  and the Terminal drawer. Use an isolated browser window or profile if the
  study can control the window Highbay opens. Capture only the study's
  surface, and keep captured frames in the tester's folder. If the tool
  cannot see Highbay's original tab, say how that limits the startup
  observation. A run needs an image of the workspace to grade the
  interface;
- **what each mid-build change needs** (see the tester brief's "Mid-build
  changes"), recorded as two separate facts: what this machine provides,
  such as model credentials or the access a live demo needs, and what the
  commit under test provides, such as an operation that regenerates code after a
  design change. The tester uses them to tell "not supported on this
  machine" apart from "blocked by a missing Highbay capability".

## 2. Put the session on the screen and record it

Use the participant's documented launch command and observe the tab it
opens. If the study can see only a separate browser, record that limitation.
Use a new link through the documented command when necessary. Treat launch
links, browser session tokens, and terminal tickets as credentials.
Keep them out of captures and transcripts.

Save frames in `attempt-<N>/screen/`, named by time and surface, and index
them in `attempt-<N>/screen/screen-log.md`. Each row gives the time, surface,
file, and exact heading or status text.

| When | Capture |
| --- | --- |
| Before sending the first request | The workspace, its primary action and available agent choices. For attachment, include the original coding session. |
| At 2, 5 and 10 seconds after sending | The visible message, any startup feedback, and which view still has focus. |
| During setup, proposal, build, and verification | Visible text every 15 seconds, with a frame for heading or status changes and at least once a minute. |
| When a role asks for help | The attention notice, what opening its Terminal displays, and what changes after the participant responds. |
| Before every human decision | The full text the participant can read, including the expanded questions, options, and examples. |
| After a click, reload or reopening | The state just before, then at about 2 and 10 seconds after the action. Record whether the browser retained sign-in and recovered the current task. |

Look at each kept frame during the round it was captured. Mark evidence of
an unexplained reset, an error message, or a control whose result is still
unclear after 10 seconds. Notify the moderator at the next relay. Quote any
text that assumes the participant already knows a framework, a starter
feature, or an internal command. Record stretches when work continues but
the workspace gives the participant no feedback for two minutes.

## 3. Start the two subagents

Read `references/tester-brief.md` and `references/participant-brief.md` and
fill in every placeholder:

| Placeholder | Value |
| --- | --- |
| `{{TASK}}` | the task |
| `{{PARTICIPANT_PROFILE}}` | the participant profile |
| `{{ENVIRONMENT_FACTS}}` | the recorded facts that matter to a user: installed tools, credentials set, network access and coding-agent sandbox restrictions |
| `{{CODING_AGENTS}}` | the coding-agent CLIs the participant may run, or "none" |
| `{{SCREEN_TOOLS}}` | The tools the participant can use to capture, read, click and type in the workspace. If the study uses an existing agent session, include how it can observe and use that session. |
| `{{PARTICIPANT}}` | the participant's folder |
| `{{TESTER}}` | the tester's folder |
| `{{CHALLENGE}}` | the tester's challenge text, word for word |
| `{{BASELINE_REPORT}}` | the path of an earlier study's `report.md` to compare with, or "none". Give it to the tester only, never the participant |

Start each role as a new subagent that does not inherit this conversation.
This conversation holds the skill, may hold Highbay's source or an earlier
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
   relaying it. If it names a Highbay command or MCP tool, a checkpoint
   (`CP1`–`CP5`), "Change Card", "checkpoint", the label of a Highbay button,
   or a Highbay doc or section, send it back to the tester to
   reword and relay nothing yet. A returned draft does not count as handed
   over. If private criteria or tester instructions ever reach the
   participant, treat it as a scope leak.
5. Append the reply to the transcript. On `continue`, send the participant
   only the questions, the hint, the change request and the screen task,
   word for word.

**While the participant waits.** Keep capturing on section 2's schedule
whether or not a report is due. The participant's brief limits it to the
screen while its agent or Highbay is working. From its actual tool calls, count
each off-screen check: a command or file read used to learn what is
happening, such as `git log`, `git status`, listing or reading project
files, or `highbay status`. Tell the tester each one, with the screen state it
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
allowed list, loading this skill, building the product without Highbay, or
compiling, generating from, or running the product's code except through
Highbay's documented operations.

A participant cannot unlearn what it read, so a leak ends the attempt. Tell
the tester what was read and when. The tester records the leak as a finding,
because reaching for the source usually means the docs failed, and marks
later milestones in that attempt as not valid. Then restart, as in section
5 step 4, with the same build. If nothing can be checked, note in the
limitations that leaks were only self-reported.

**When an attempt or the session ends**, do two things:

- In the workspace's **Team** view, stop only the managed role terminals
  this attempt started. Then run `highbay stop` in the attempt's project. For
  any other process the participant reported, such as a Temporal dev server,
  check that its PID still matches the command and folder from this attempt
  before stopping it. Leave other projects and shared companion services
  running.
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
Highbay bug. If someone later asks for cleanup, inspect each exact target
first: nested repositories, `git status`, unpushed commits, and whether
deleting it is authorized.

## 5. Handle a blocking bug

When the tester decides `stop_for_bug`, it names the bug and gives the steps
to reproduce it. A blocking bug is one where Highbay crashes, behaves
differently from its documentation, or lacks an MCP/browser operation its
documented workflow needs, and no documented workaround exists. A terminal
workaround can unblock the product task while still failing the zero-command
journey goal; record both outcomes rather than silently counting success.

1. Reproduce the bug yourself with the trusted Highbay CLI, using the host and
   network access you are authorized to use, so a restriction in your coding
   agent is not mistaken for a Highbay bug. The product's code is still
   compiled, generated from, and run only through Highbay's documented operations.
   - If the failure comes from the harness, tell the tester "environment
     limitation, not a Highbay bug" and which milestones it affects.
   - A missing capability, meaning no documented Highbay operation for a step the
     documented workflow needs, counts as a real bug even when Highbay lacks
     it by design. Go to step 2.
   - If Highbay behaves as designed, tell the tester "reproduced; behavior is as
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
   `go list -m github.com/goadesign/highbay@<sha>`; update a documented version
   pin when that is what selects the dependency. Do not patch the participant's
   project or supply an undocumented override to make the attempt pass.
4. Restart from scratch:
   1. Stop processes and record leftovers as in section 4.
   2. Create a new participant folder, `~/src/highbay-ux-<id>-a<N+1>/`, holding
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

Report separate outcomes for installing and setting up Highbay, building,
verifying, approving, and merging the product. Identify participant
commands, agent commands, harness invocations, and steps completed with
help. Record the path exercised and the evidence for each step. Where
applicable, cover starting a guide from the browser, attaching an existing
guide, sending a message and seeing a reply, answering a Terminal prompt,
reloading during work, reviewing a proposed revision, and recovering from a
refused merge. Include which agent choices were available and whether the
configured tests called for a test writer. Use the moderator's transcript and
screen evidence to support each severity rating.

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
