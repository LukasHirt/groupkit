<!--
SPDX-FileCopyrightText: 2026 groupkit contributors

SPDX-License-Identifier: Apache-2.0
-->

# The file store

`store/filestore` is a `store.Store` that keeps calendars and address books
as plain files in one directory. It needs no database and no extra software.
This document describes what it writes to disk, what it promises after a
crash, and what an operator can do when something is wrong.

## What it guarantees

- Every object is a file that holds exactly the bytes the client sent. You
  can read, copy and back up the files with ordinary tools.
- A write that returned without an error is on disk. The store flushes the
  log, the data and the directory entries before it returns.
- A write that returned an error, or that was cut off by a crash, is undone
  on the next load, unless its data was already in place. Then it is kept,
  like a write whose reply was lost in a crash.
- After a restart, collections, objects, UIDs, revisions and the change
  journal are as they were.
- Readers and a crash never see a half-written object or collection.
- The store never guesses. If the files disagree with the log in a way a
  crash cannot explain, it refuses to load that collection and tells you
  which object is affected. Other collections keep working.

## Opening and closing

```go
s, err := filestore.Open("/var/lib/example/data",
	filestore.WithMaxJournal(10000),
	filestore.WithLogger(logger),
)
if err != nil {
	return err
}
defer s.Close()
```

`Open` creates the directory if it does not exist, takes the lock, and checks
`format-version`. It does not read any collection. A collection is loaded and
checked the first time a call uses it, and then stays in memory until
`Close`.

`OwnerDir(owner)` returns the directory of one owner, so a program that embeds
the store can keep its own files next to the owner's data. It only computes
the path. It returns `""` for an owner that `store.ValidateOwner` rejects.

The store works on Unix systems. On other platforms `Open` returns an error,
because the store cannot lock the directory there.

## Directory layout

A calendar `work` owned by `alice@example.com`, holding two events named
`standup.ics` and `Review 1.ics`, looks like this:

```
data/
├── format-version
├── lock
└── owners/
    └── alice%40example.com/
        └── calendars/
            └── work/
                ├── collection.json
                ├── log
                ├── objects/
                │   ├── %52eview%201.ics
                │   └── standup.ics
                └── tmp/
```

| Path | What it is |
|---|---|
| `format-version` | The version of the layout, currently `1` followed by a newline |
| `lock` | An empty file that the open store holds a lock on |
| `owners/<owner>/` | One directory per owner |
| `owners/<owner>/calendars/` | The owner's calendars, one directory each |
| `owners/<owner>/addressbooks/` | The owner's address books, same shape |
| `<collection>/collection.json` | The collection's ID, names and properties |
| `<collection>/log` | The change log, one line per write |
| `<collection>/objects/` | One file per object, holding its data |
| `<collection>/tmp/` | Scratch space for writes in progress |

The store only manages `calendars/` and `addressbooks/` inside an owner's
directory. Anything else there belongs to the program that embeds the store
and is left alone.

The real names of owners, collections and objects are stored inside
`collection.json` and the log. The store never reads a name back from a
directory or file name.

## How names become file names

Owner, collection and object names are turned into file names by one rule.
Two different names never get the same file name, even on a file system that
ignores case.

- The bytes `a` to `z`, `0` to `9`, `-` and `_` are kept as they are.
- A `.` is kept, unless it is the first or the last byte of the name.
- Every other byte becomes `%` and two upper-case hex digits. This covers
  upper-case letters, spaces, `%`, `~`, and each byte of a non-ASCII
  character.
- If the part before the first `.` is a reserved device name on Windows
  (`con`, `prn`, `aux`, `nul`, `com1` to `com9`, `lpt1` to `lpt9`), its first
  letter is escaped too.
- If the result is longer than 200 bytes, the file name is `~` followed by
  the 64 hex digits of the SHA-256 of the name. A `~` is always escaped in
  the other case, so the two forms never collide.

Upper-case letters are escaped because a case-insensitive file system would
treat `A.ics` and `a.ics` as one file. Non-ASCII bytes are escaped because
some file systems treat different byte sequences for the same character as
one name.

| Name | File name |
|---|---|
| `event.ics` | `event.ics` |
| `A.ics` | `%41.ics` |
| `.hidden` | `%2Ehidden` |
| `con.ics` | `%63on.ics` |
| `alice@example.com` | `alice%40example.com` |
| `Ünïcode.ics` | `%C3%9Cn%C3%AFcode.ics` |
| 210 times `x`, then `.ics` | `~ebf90de88092848e315d4329e346bcf501ee9a27f38d68fde97933ff9185e7f1` |

## collection.json

```json
{
  "ID": "e6421d4648215978fdf6a55e0f33236b",
  "Owner": "alice@example.com",
  "Kind": "calendar",
  "Name": "work",
  "Created": "2026-10-29T16:20:32.430729Z",
  "Props": {
    "DisplayName": "Work",
    "Description": "",
    "Color": "#3366ccff",
    "Order": 0,
    "Timezone": "",
    "Components": [
      "VEVENT"
    ]
  },
  "Revision": 0,
  "Floor": 0
}
```

| Field | Meaning |
|---|---|
| `ID` | 128 random bits in hex. A new collection always gets a new ID, also when it reuses the name of a deleted one |
| `Owner`, `Kind`, `Name` | The collection's real owner, kind (`calendar` or `addressbook`) and name |
| `Created` | When the collection was created, in UTC |
| `Props` | The properties clients set: display name, description, colour, order, time zone and accepted components |
| `Revision` | The revision of the last property change |
| `Floor` | The oldest revision the change journal can still answer for |

The file is replaced as a whole when a property changes and when the log is
compacted. Object writes do not touch it, so `Revision` here is often behind
the log. The collection's revision is the larger of this value and the
revision of the last log record.

## The change log

`log` has one JSON object per line. Each line records one write:

```
{"r":1,"n":"standup.ics","e":"e7b90ba593ee6a70c470f4e0fdd2d032","u":"standup@example.com","s":96,"m":1793290832460697000}
{"r":2,"n":"Review 1.ics","e":"9f7cb79e5d5697d152e7bce8401be284","u":"Review-1","s":85,"m":1793290832475846000}
{"r":3,"n":"standup.ics","d":true,"m":1793290832489821000}
{"r":4,"n":"standup.ics","e":"e7b90ba593ee6a70c470f4e0fdd2d032","u":"standup@example.com","s":96,"m":1793290832494229000}
```

| Key | Meaning |
|---|---|
| `r` | Revision. Every line has a higher revision than the line before |
| `n` | The object's real name |
| `d` | `true` for a delete. Left out otherwise |
| `e` | The ETag: the first 32 hex digits of the SHA-256 of the data |
| `u` | The UID, if the object has one |
| `s` | The size of the data in bytes |
| `m` | The modification time in Unix nanoseconds. The object's file gets the same time |

The sample above creates `standup.ics`, creates `Review 1.ics`, deletes
`standup.ics` and creates it again. The latest line for a name describes the
object as it is now. The log is also the change journal that `Changes`
answers from.

A write with the same bytes and the same UID as the stored object changes
nothing: it adds no line and does not raise the revision. Property changes
raise the revision without adding a line.

## Write order

A put happens in this order, with the collection locked:

1. Append the record to `log` and flush it to disk.
2. Write the data to a new file in `tmp/`, set its modification time to the
   record's, flush it, rename it into `objects/`, and flush the directory.
3. Update memory and return.

A delete appends a record with `"d":true`, flushes it, and then removes the
file. Once the record is on disk the delete has happened. If the file cannot
be removed, the delete still succeeds. The store reloads the collection on
its next use and tries the removal again. If nothing else was written in
between, the delete record is still the last line of the log, and the reload
removes the file.

A file that is still there after that is ignored by reads and listings. A
later successful put of the same name replaces it. While the delete record is
in the log, a later put of that name that stops before its data is in place
is rolled back as usual. Compaction drops delete records at or below the
journal floor. After that, if a put of the name is interrupted while the
file is still there, recovery reports the collection as damaged. To fix it,
delete the file by hand. It is at `objects/<file name>` in the collection
directory.

The log goes first because it makes every crash explainable. Writes to one
collection never overlap, so at most one write can be in flight when the
process dies, and that write is always the last line of the log. On the next
load the store only has to compare that one line with the disk. If the data
went first, a crash could leave a file that no record describes, or an
object's file holding bytes its record does not match, and the store could
not tell an interrupted write from damage.

If step 2 fails, the call returns an error and the store drops the
collection from memory. The next call loads it again from disk, and recovery
undoes the record from step 1.

## What happens after a crash

Recovery runs every time a collection is loaded. It reads the log, decides
what to do about the last line, and checks every live object against its
file. A file passes when its size matches the record and its modification
time is within two seconds of the record's. When the time is further off, the
store hashes the file and accepts it if the ETag matches. The file of the
last record is always hashed.

| What the store finds | What it does |
|---|---|
| The last line is cut off or has no newline | Ignores it and cuts the log back to the last complete line |
| The last record matches its file | Keeps it. The write finished and only the reply was lost |
| The last record is a put, and the disk still shows the state before it | Removes the record from the log. Revisions continue as if the write never happened |
| The last record is a put of a name whose previous record is a delete, and a file other than the put's is there | Treats the file as one a failed removal left behind: removes the record from the log and leaves the file alone |
| The last record is a delete and the file is still there | Removes the file |
| The last record matches neither its file nor the state before it | Refuses to load the collection |
| A complete line (one that ends with a newline) does not parse, or its revision does not increase | Refuses to load the collection |
| Any other live object has a missing file, a wrong size, or different content | Refuses to load the collection |
| Files in `objects/` that no record describes | Leaves them alone and never serves them |
| Files in `tmp/` | Removes them |
| Leftover `.staging-*` and `.trash-*` directories next to collections | Removes them the next time the owner's collections of that kind are listed |

The store changes nothing on disk unless the whole collection checks out.

Reading an object also hashes its data. If the bytes on disk no longer match
the record, the read returns an error wrapping `ErrCorrupt` instead of the
data.

### Known limit

If the newest record of a collection creates an object and that object's
file is lost later, the disk looks exactly like a create that never finished.
The store rolls the record back, and the object disappears without an error.
The same happens when the newest record overwrites an object and its file is
later put back to the previous content. The store cannot tell these cases
apart from an interrupted write, because only the newest record can be in
flight and it is checked against the disk alone.

## When a collection refuses to load

"Refuse to load" means every call on that collection returns an error that
wraps `ErrCorrupt` and names the collection directory and the object.
`ListCollections` leaves the collection out and reports it to the logger set
with `WithLogger`. Other collections are not affected.

The store does not repair this itself, because any repair would throw away
data a user may need. Stop the program first, then do one of the following.

Restore the file from a backup. Put the object's file back into `objects/`
under its encoded name, with exactly the bytes the log records. The ETag in
the log is the first 32 hex digits of its SHA-256. The modification time does
not need to match: a file whose time is off is hashed and accepted if the
content matches.

Or accept the loss and edit the log. Append a delete record for the object at
the end of the log:

```
{"r":5,"n":"Review 1.ics","d":true}
```

The revision must be higher than the last revision in the log, and should be
higher than `Revision` in `collection.json`. End the file with a newline and
do not leave blank lines: an empty line counts as damage. On the next load
the object is gone, and clients that sync see it as deleted.
The store removes the object's file if one is still there, so move it aside
first if you want to keep it.

A line in the middle of the log that does not parse has to be fixed or
removed by hand. Keep the revisions increasing.

## Locking

`Open` takes an exclusive, non-blocking `flock` lock on `lock` in the root
directory and holds it until `Close`. A second `Open` on the same directory
returns `ErrLocked`, whether it comes from another process or from the same
one. The lock goes away when the process exits, so a crash does not leave the
directory locked.

Run one process per directory. The lock only keeps out other stores. It does
not stop other programs from changing the files, and changing them while the
store runs is not supported.

Inside one process, writes to a collection are serialized, and reads of a
collection run in parallel. Different collections do not block each other.

## Compaction and the journal limit

The change journal keeps the newest records of each collection, up to the
limit set with `WithMaxJournal` (10000 by default). When a write pushes the
journal past the limit, the oldest records leave it and `Floor` rises to the
highest revision that left. `Changes` with a revision below the floor, or
above the collection's revision, returns `store.ErrChangesUnavailable`, and
the client has to sync again from scratch. Revision 0 always lists every
object.

The log is compacted after a write once it has more lines than twice the
limit plus the number of live objects. The new log holds one record for each
live object whose latest record is at or below the floor, followed by the
journal. Deletes at or below the floor are dropped. The last line stays the
same, byte for byte.

Compaction writes `Floor` to `collection.json` first. Then it writes the new
log to `tmp/`, flushes it, renames it over `log` and flushes the directory.
A crash between the two steps leaves a higher floor with the old log, which
loads fine. If compaction fails, the write that triggered it still stands,
the old log stays in place, the failure is logged as a warning, and the next
write tries again.

The floor in memory rises right away, but on disk it only moves when the log
is compacted. After a restart the journal can therefore hold more than the
limit. The limit is a minimum.

## Permissions

The store creates directories with mode `0700` and files with mode `0600`,
so only the user the program runs as can read the data. The process umask
can only make these stricter. Run the program as a dedicated user that owns
the directory.

## Backing up

Stop the program and copy the directory, for example with `rsync -a`. Or take
a snapshot of the file system that holds it. A copy made with ordinary tools
while the store is running can catch a log line without its file, or files
from different moments in different collections. A snapshot does not have
that problem, because it captures the whole file system at once, just like a
crash would.

To restore, put the directory back and start the program. The `lock` file
can be copied along; it holds no state.

## format-version

The root holds a file `format-version` with the content `1` and a newline.
`Open` writes it into a new directory and refuses to open a directory with
any other value, so a build never reads a layout it does not understand. Do
not edit it.

## Options

| Option | Effect |
|---|---|
| `WithMaxJournal(n int)` | How many records the change journal keeps per collection. Values below 1 are ignored. The default is 10000 |
| `WithLogger(l *slog.Logger)` | Where the store reports problems it works around: a damaged collection that `ListCollections` leaves out, and a compaction that failed. A nil logger is ignored. By default nothing is logged |

## Errors

| Error | Meaning |
|---|---|
| `ErrLocked` | `Open` found the directory locked by another store, in this process or another one |
| `ErrCorrupt` | A collection's files disagree with its log in a way a crash cannot explain. The message names the collection directory and the object |
| `ErrClosed` | The store was closed. Calls made after `Close` return it, except that argument validation and a cancelled context are checked first. A second `Close` returns the result of the first |

The store also returns the errors defined by the `store` package, such as
`store.ErrNotFound`, `store.ErrExists`, `store.ErrPreconditionFailed` and
`store.ErrChangesUnavailable`, with the meaning given there.

## Limits

- Collection and object names can be up to 255 bytes, and an owner up to
  1024 bytes, as `store.ValidateName` and `store.ValidateOwner` allow. Long
  names are stored under a hashed file name, so the file system's own limit
  on name length does not matter.
- Every loaded collection stays in memory until `Close`: one entry per live
  object and the journal. The data itself is read from disk on each read.
- Loading a collection reads its whole log and checks every object's file.
- A loaded collection keeps its log file open. A program that touches
  thousands of collections needs a file descriptor limit to match.
- A collection is loaded while the store's map of collections is locked, so
  one slow load delays other requests. This is known and will be improved.
- Every write flushes to disk before it returns. There is no option to trade
  that for speed.
