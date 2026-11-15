---

## 10. 📜 The Golden Rules: Working with Codegen

* ✍️ **Design First:** The guide changes the design in its draft and proposes a revision for the user to adopt. The builder implements that agreed design in its working copy and asks the guide about any design change.
* 🔄 **Regenerate:** The builder calls `generate_code` to write the agreed `design/` and regenerate `gen/`; it edits neither directory. When a build runs with a test writer (without stored verification settings), the `eval/` test module belongs to the test writer, which reports to have Surla generate it again, or runs `surla gen -dir eval` from a shell that is not sandboxed; outside such a build, the builder can regenerate `eval/` with `generate_code` and `target: "eval"`. Surla's Story table stays in place through regeneration.
* 🧪 **Check Before You Commit:** The builder calls `check_project` to build and test the project's uncommitted files.
* 🚫 **Hands Off `gen/`:** Never edit the `gen/` directory by hand. Your changes will be overwritten!

