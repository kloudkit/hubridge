# Never print a secret value

Not to the terminal, not to logs, not into a file. Item names, field names and secret-store
paths are fine; values are not.

Filter JSON and environment output to the keys you need before printing (`jq` selects, `env`
by exact name); never dump a whole settings object, environment block or config file.

Never go looking for credentials: enumerating what a process's environment, a git remote or a
rendered secret exposes is exfiltration, whatever the intent. If one surfaces incidentally,
say a credential was visible and name its location, never its value.
