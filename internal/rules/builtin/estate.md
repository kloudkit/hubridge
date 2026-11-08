# Estate

Estate is the operator's knowledge base in hubd: claims (one fact, trap, rule or intent each)
and how-tos (ordered steps toward one task). Read it only through hubd's MCP tools; there are
no estate files on this machine.

- **Find:** `estate_search` by words, topic (or one of its aliases) or type; traps come
  first. `topics_list` names the topics, their aliases and what each covers.
- **Read:** `estate_get` with a path from the search (`claims/<slug>.md`, `howto/<task>.md`).
  It returns the body, its `rev` and the links in and out. Pass `session` with your Claude
  Code session id to both tools.
- **A path like `claims/<slug>.md` or `howto/<task>.md`** in a skill, a claim or a how-to is a
  path for `estate_get`.

## Claims are the last observation, not the truth

A claim records what was true when it was last verified (`verified.at`). Before acting on a
claim that matters, re-observe: run its ```check block, or the command its body names. When
reality disagrees, reality wins: act on what you observed, say so, and propose a correction.
A claim past `stale_after`, or unverified, is a lead, not an answer.

## Rules

- A probe that cannot run, or one failed command, proves nothing either way: the fact is
  unknown.
- "Not in estate" is a gap, not evidence that there is nothing to know.
- Do not infer topology, credentials or naming from the repo you are in.
- Change estate only through `estate_change`, passing the `rev` you read as `base`. hubd
  checks every change; it waits for the operator in hubd unless the operator approved that
  exact diff in this chat. Propose a change whenever the operator, a failing command or your
  own judgment shows estate is missing something or wrong.
