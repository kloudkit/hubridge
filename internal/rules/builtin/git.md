# Other sessions share the checkouts: commit with a pathspec, never stash

Other sessions may work in the same checkout and leave files staged in the shared index. A
plain `git commit` takes the whole index. Commit with an explicit pathspec
(`git commit -m … -- <paths>`), check `git diff --cached --name-only` first, and stop if
anything unexpected is staged. Never `git stash`: it moves another session's work. Never
stage changes you did not make.
