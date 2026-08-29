# Artemis

The deploy proxy for freeCodeCamp static apps. It owns the bytes of every static site and
the names those sites answer to. This file fixes the language; it is a glossary, not a spec.

## Language

### The things

**Site**:
A named static app artemis stores bytes for. Identified by a slug.
_Avoid_: app, project

**Slug**:
The short registry name of a site, such as `teleprompter`.
_Avoid_: name, id

**Dirname**:
The storage name of a site, such as `teleprompter.freecode.camp`. One slug maps to exactly
one dirname.
_Avoid_: prefix, bucket path, folder

**Deploy**:
One immutable set of files for one site, identified by a deploy id. A site has many.
_Avoid_: build, version, release

**Alias**:
A pointer from a site's `production` or `preview` mode to one deploy id. Changing it is what
makes a deploy live.
_Avoid_: pointer, symlink, current

**Marker**:
The object a finished deploy carries to prove it completed. Its absence is how a partial
upload is told from a whole one.
_Avoid_: manifest, sentinel

### The lifecycle

**Unpublish**:
Removing a site's aliases, so it stops answering. Touches no bytes.
_Avoid_: take down, disable

**Reserved**:
The state a site's name holds after a delete, for a grace window, so nobody else can take
it. The bytes are still there.
_Avoid_: held, locked, parked

**Undelete**:
Reversing a reservation before it expires. Restores the aliases and makes the site active.
_Avoid_: restore, recover, undo

**Reclaim**:
Moving a whole site's bytes to trash. Runs nightly, once a reservation has expired.
_Avoid_: purge, wipe, clean up

**Release**:
Ending a reservation immediately by request, reclaiming the bytes and freeing the name. The
name is freed last.
_Avoid_: force delete, purge

**Tombstone**:
A record that bytes were moved to trash, plus the move itself. Reversible until the purge.
Applies to one deploy, or to a whole site.
_Avoid_: soft delete, mark deleted

**Trash**:
Where tombstoned bytes wait out the recovery window.
_Avoid_: bin, quarantine, archive

**Purge**:
The permanent delete of trashed bytes once the recovery window has passed. This is the only
sense of the word. Nothing that keeps the bytes is a purge.
_Avoid_: hard delete, expunge

### Drift

**Drift**:
Any disagreement between the bytes, the index and the registry.

**Reindex**:
Writing the missing index row for a deploy whose bytes and marker are both present. Repairs
drift without moving anything.

**Orphan**:
Bytes with no index row. Always say which scope is meant: an orphan deploy, or an orphan
alias, which is an alias serving a site the registry does not know.
_Avoid_: orphan, unqualified

**Prune**:
Deleting an index row whose bytes are gone. Touches no bytes, because there are none.

**Reconcile**:
The sweep that finds drift. It reports; a person repairs. It is a permanent backstop, not a
scheduled repair.
_Avoid_: repair, self-heal, GC
