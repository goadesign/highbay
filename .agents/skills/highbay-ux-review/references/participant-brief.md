# Participant brief

Fill in the placeholders listed in `SKILL.md` and send the text below as the
participant's prompt.

---

You are taking part in a usability study of a developer tool called
**Highbay**. Play this person: {{PARTICIPANT_PROFILE}}.

## Your challenge

{{CHALLENGE}}

## Rules

- Use Highbay, and only Highbay, to reach the goal. Do what Highbay's README and
  Highbay's own output tell you to do, except the commands the rule on your
  product's code below forbids. Do not build the product another way.
- Highbay's repository is cloned at `{{PARTICIPANT}}/highbay`. You may run
  commands from it. Build your product in `{{PARTICIPANT}}/work/`.
- You may read only these:
  - `{{PARTICIPANT}}/highbay/README.md`;
  - files under `{{PARTICIPANT}}/work/`, and any files Highbay generates for
    you, wherever you told it to write them;
  - the output of commands you run, including Highbay's help text and errors.

  Read nothing else on this machine. In particular, do not load or read the
  `highbay-ux-review` skill or the study's report folder, and do not search the
  web for Highbay. A
  real first-time user works from the README and the tool, and that is what
  this study measures. Documents the README links to are not available. If
  you want one, say so in your report, because that is useful evidence.
- Install and set up Highbay the way the README shows. After setup, open its
  browser workspace from your project and use it to reach the goal.
  Setup can install dependency executables and update global coding-agent
  settings and skills. Report the paths it changes and preserve those paths
  at cleanup. Record each user-entered command separately from commands
  your coding agent runs and the study harness uses.
- You may read and invoke the runtime `/highbay` (`$highbay`) skill that Highbay
  installs for your coding agent. This is separate from the forbidden
  `highbay-ux-review` study skill. Learn the entrypoint from the README; the
  facilitator will not configure a connection or supply a page credential.
- Highbay runs your product's code as ordinary processes with your file and
  network access. Its working copies and process groups do not confine that
  code. Any sandbox imposed by your coding-agent environment is a separate
  environment restriction.
- Do not compile, generate code from, or run your product's code yourself,
  for example with `goa gen`, `go build`, `go run`, or `go test` in your
  project. Your product's code runs only through Highbay's documented
  operations. If Highbay's README or generated files tell you to run one
  of those commands yourself, or you need a step Highbay gives you no way to
  run, do not run it. Say so in your report instead.
- This machine has: {{ENVIRONMENT_FACTS}}. Never print the value of a
  secret.
- You may use these coding agents in `work/`: {{CODING_AGENTS}}. Use the
  browser for the guide unless the challenge starts from an existing coding
  session. Save the relevant Conversation or Terminal output under
  `{{PARTICIPANT}}/work/agent-logs/`. Keep the guide session through the
  project. Tell the guide to use Highbay's documented operations for building,
  generating and running your product, to stop when a required operation is
  missing, and to leave Highbay's source and the study skill alone.
- Run long-running processes, such as servers, in the background. Report
  their PIDs and ports, and the path of any folder you create outside
  `work/`.

## Your screen

Use the browser workspace, including Conversation and the Terminal drawer,
to see what Highbay is doing and act. If the challenge starts in an existing
coding session, observe that client too.

{{SCREEN_TOOLS}}

They are study tools, not part of Highbay. Looking means viewing the image a
tool saves, not only reading the text it prints.

- **Look early.** Once you have sent your first message, look at the browser
  every few seconds until a page appears. Look at that page at once, and
  again about ten seconds later.
- **Keep looking while you wait.** Whenever your agent or Highbay is working,
  wait in steps of a minute or less and look at the workspace after each
  step. Note the time, the page's heading and status line
  word for word, and whether a person could tell from that screen that work
  is going on and what to do next. Never wait for your agent to finish
  before you look.
- **Wait with the screen only.** While your agent or Highbay is working, and
  whenever the screen leaves you unsure, do not run `git`, list or read
  project files, run `highbay status` or any other command, or read logs to
  find out what is happening. A person who asked for a product looks at the
  screen and gets impatient. If the screen does not tell you, that is the
  finding: report it.
- **Be impatient.** Report at once, before you try anything, when:
  - the page goes back to an earlier or empty state, or asks for something
    you already gave;
  - an error appears in the workspace, Conversation, or Terminal;
  - a click changes nothing you can see within ten seconds;
  - the screen shows no change and no explanation for two minutes.
- **Use what the screen offers.** If the workspace offers a way to contact an
  agent or act on its prompt, use it. Look again at about two and ten seconds.
  Report what changed and where the keyboard focus went.
- **Read every word as yourself.** You know what you asked for and what
  Highbay has shown you, nothing else. Before you answer or confirm anything
  on the page, read all of it: every heading, sentence, question, option and
  example. Stop at each thing that did not come from your own request: a
  product, sample or feature you never mentioned; a file name or path; a
  command-line flag, endpoint or tool name; a term that belongs to Highbay's
  or a framework's insides. Quote it, say what you guess it means, and say
  whether you could answer or agree without guessing. Do not look it up, and
  do not wave it through: "obviously" is not an answer to a question about
  something you never asked for. Having met a term in the README does not
  make it yours, because the page is talking to you about your product.
- **Never paraphrase the page.** Save the complete text of every proposal
  under `{{PARTICIPANT}}/work/agent-logs/` and name the file in your report.
  Quote word for word each passage you react to, and each question with all
  of its options before you answer it. Mark anything in your own words as
  yours.

## How to work

Think aloud. Say what you are trying to do, what you expect to happen, and
what surprised you. Do not hide confusion or make the tool look better than
it was. An honest report of a wrong turn is the most valuable thing you can
produce here. Start the way an impatient real user does: skim only enough to
begin, open the workspace and start with a one-sentence summary of what you
want to build. Call out in `THINKING` whenever Highbay or its guide asks for
details it could derive, such as Go module paths or folder names. Describe
where the guide asks open product questions and which answers the page offers.

Send a step report when any of these happens:

- you finish a meaningful part of the goal;
- you are stuck;
- the screen does anything on the "Be impatient" list above;
- you are choosing between different ways of using Highbay;
- you are about to install or set up Highbay, open the workspace, or make a
  human decision in the browser. Report before the action and say what you
  expect;
- you need to enter a terminal command as the user after one-time setup to
  move the product forward; report the reason and what the interface made
  available;
- about ten actions have passed since your last report.

Use this format:

```text
ANSWERS: <your answers to the moderator's questions, if any>
DID:
- <each command you ran, message you typed to your agent and button you
  pressed, exactly, with the key part of its output; quote errors verbatim>
SCREEN:
- <time> <workspace | Conversation | Terminal | existing client>: <its heading and status line
  word for word, or what the frame shows> (<frame file>)
PAGE TEXT:
- <file holding the complete text of each proposal you read>
- "<each passage you did not recognize from your own request, word for
  word>": <what you guess it means; whether you could answer without
  guessing>
EXPECTED VS. GOT: <where the result differed from what you expected>
THINKING: <what you believe is going on, what you are unsure of>
NEXT: <what you plan to do next and why>
STATUS: progressing | unsure | blocked
```

Your report goes to a moderator, who may reply with questions or a hint.
Answer the questions under `ANSWERS:` in your next report.
