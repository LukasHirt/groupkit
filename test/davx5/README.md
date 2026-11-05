<!--
SPDX-FileCopyrightText: 2026 groupkit contributors

SPDX-License-Identifier: Apache-2.0
-->

# DAVx5 check

`check.py` drives DAVx5 in an Android emulator against the example server through `adb`.
It installs the DAVx5 APK, opens DAVx5's login screen with a `caldav://` link, taps
through account creation and then checks, with the content providers that the Calendar
and Contacts apps use, that:

- an event and a contact written on the server reach the phone;
- an event and a contact created on the phone reach the server;
- a delete on the server removes the event from the phone, and a delete on the phone
  removes the event from the server.

`run.sh` starts the example server on the host's loopback address, runs `check.py` and
stops the server. The emulator reaches the host as `10.0.2.2`. The `DAVx5` workflow runs
it nightly and on demand, and when files in this directory change.

## Best effort

This check is a UI test on an emulator. It can fail for reasons unrelated to the server:
a slow boot, a screen that DAVx5 changed in a newer release, a dialog the script does
not know. It is therefore not part of the `CI` workflow and not a required check. The
APK is pinned by version and checksum, so a new DAVx5 release does not change the result
until the pin is moved. When a run fails, the `davx5-output` artifact holds the server
log, the device log and a screenshot, and the script prints the text visible on the
screen at the failing step.

## Run it

Start an emulator (an x86_64 image on Linux with KVM, or an arm64 image on Apple
silicon, Android 14 worked), then:

```sh
go build -o /tmp/groupkit-example ./examples/server
curl -fsSLo /tmp/davx5.apk https://f-droid.org/repo/at.bitfire.davdroid_405210002.apk
SERVER=/tmp/groupkit-example DAVX5_APK=/tmp/davx5.apk sh test/davx5/run.sh
```

The script creates an account in DAVx5 and changes the phone's calendar and contacts.
Use a throwaway emulator. Set `ANDROID_SERIAL` if more than one device is attached.
