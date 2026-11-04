// SPDX-FileCopyrightText: 2026 groupkit contributors
//
// SPDX-License-Identifier: Apache-2.0

module.exports = {
  extends: ["@commitlint/config-conventional"],
  // Dependabot and release-please bodies contain long release-note and compare
  // URLs that exceed body-max-line-length, and we can't change how they format
  // them.
  ignores: [
    (message) => /^Signed-off-by: dependabot\[bot\]/m.test(message),
    // The release pull request commit lists every change with its compare link.
    (message) => /^chore\(main\): release \d/.test(message),
  ],
};
