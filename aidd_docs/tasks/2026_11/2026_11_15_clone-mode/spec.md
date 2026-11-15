# Podbox — Clone mode

## Target

A developer can run an agent on untrusted code in a podbox sandbox whose workspace is the agent's own git clone of the project, never the host working tree, review what the agent changed, and bring it back to the host as a branch or a patch, without the agent ever touching the host repository.

## Hard constraints

- `--clone` is an opt-in flag of `create` and `run`; `shared` stays the default on every OS. A sandbox's mode is fixed at creation and shown by `ls`, in text and JSON.
- The clone is made from the host repository's committed HEAD only. Uncommitted and untracked host files are not carried, and the human is told how many were left out.
- The clone shares no objects with the host repository, so the agent cannot alter the host's object store. It carries no host remote, no host git configuration, no host hook and no credential.
- The clone lives in podbox's own state and appears in the agent at the project's usual absolute path, so tools and paths match shared mode.
- The project must be a git repository with at least one commit; otherwise `--clone` is refused with a clear message and a usage exit code.
- Git is needed on the host only for `--clone`; `doctor` reports its absence as optional, never as a failure.
- Masks, `.sandboxignore`, the network policy, the agent context and every container hardening guarantee of shared mode hold unchanged on a clone. A masked file that is tracked stays masked in the clone; an untracked secret is absent from it.
- The git protections of shared mode (read-only hooks and config, fingerprint, `git-check`, hook notices) do not apply to a clone and are switched off in one single place (`internal/gitguard`). `git-check` on a clone sandbox says it does not apply and does not fail.
- The host never runs git inside the agent's clone and never trusts its configuration. `diff` and `export` get their data from git run inside the sandbox, which therefore must be running (podbox starts it or says so), and the host ingests only inert data, a bundle or patch text, which it verifies.
- `podbox diff <name>` shows what the agent changed since the clone's base commit: the changed files with their counts, then the patch, covering commits, uncommitted tracked changes and untracked files. `--stat` shows only the summary. Terminal control sequences are removed from everything the agent wrote before it is printed. `--output json` returns the file list with status and counts, and the sanitized patch.
- `podbox export <name> --as branch` creates a new branch in the host repository from the agent's commits, through a verified bundle. The default name is `podbox/<sandbox>/<agent branch>`; `--branch-name` overrides it. It never checks the branch out and never changes the host working tree, index, other branches or configuration. It refuses to overwrite an existing branch and suggests another name.
- `podbox export <name> --as patch` writes a patch series file, by default `<sandbox>.patch` in the current directory (`--out` overrides), and refuses to overwrite an existing file.
- Only committed work is exported. Uncommitted and untracked work is reported as not exported. Nothing is ever merged automatically. `--output json` reports what was created.
- `rm` of a clone sandbox keeps the clone unless `--volumes` is passed, because it holds the agent's work, and prints where it is kept. `create` on a name whose clone still exists is refused, with the command that removes it.
- Exit codes follow the existing table; none is added unless unavoidable, in which case the plan states which and why. Every new command accepts `--output json`.

## Non-goals

- Pulling host changes into an existing clone.
- Pushing from the sandbox.
- Exporting uncommitted work.
- Converting a shared sandbox into a clone, or the reverse.
- Cloning from a remote URL.
- Submodule and LFS support beyond what a plain clone gives; the limit is documented.
- `cp`, `port` and kits.
- Verification on macOS and Windows beyond design.

## Done-when

- A clone sandbox contains only committed content and no host remote, configuration, hook or shared object, and a tracked masked file is masked in it.
- After the agent commits in the clone, the host repository, its working tree and its hooks are byte-identical to before.
- `diff` shows committed, uncommitted and untracked changes, and a file name or content carrying terminal control sequences is printed without them.
- `export --as branch` creates the branch with the agent's commits, refuses an existing name, and leaves the host working tree and current branch untouched.
- The patch written by `export --as patch` applies cleanly with `git apply` on a fresh checkout of the base commit.
- Uncommitted work is reported as not exported by both export forms.
- `ls` shows the mode of each sandbox; `rm` keeps the clone unless `--volumes`; `create` refuses a name whose clone remains; `git-check` on a clone says it does not apply.
- Shared sandboxes behave as before: the existing suites pass unchanged.
- README and `docs/security-model.md` describe the clone mode, its limits and when to use it.
- All of the above is verified by the Linux e2e suite on every change.

## Open Questions

None.

## Context

- The v0.1 spec lists clone mode, `diff` and `export` as non-goals (`aidd_docs/tasks/2026_11/2026_11_14_podbox-v0-1/spec.md`); `docs/security-model.md` already points untrusted work to this mode.
- Delivery is three stacked pull requests, each based on the previous one: clone mode, then `diff`, then `export`.
- The data of `diff` and `export` is produced inside the sandbox on purpose: a git directory the agent controls can name commands the host's git would run.
