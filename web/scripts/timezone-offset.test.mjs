import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { fileURLToPath } from "node:url";
import ts from "typescript";

// Compile the production module in memory. This keeps the regression test
// portable and avoids adding a second frontend test runner.
const sourcePath = fileURLToPath(new URL("../src/lib/timezone.ts", import.meta.url));
const source = await readFile(sourcePath, "utf8");
const compiled = ts.transpileModule(source, {
  compilerOptions: { module: ts.ModuleKind.ESNext, target: ts.ScriptTarget.ES2022 },
});
const momentBuild = "moment-timezone/builds/moment-timezone-with-data-10-year-range.js";
const momentURL = import.meta.resolve(momentBuild);
const output = compiled.outputText.replace(`from "${momentBuild}"`, `from "${momentURL}"`);
const moduleURL = `data:text/javascript;base64,${Buffer.from(output).toString("base64")}`;
const { timeZoneNames, timeZoneOffsetLabel, zonedDateParts } = await import(moduleURL);

// Simulate the extension responsible for the bug: every native local offset
// becomes zero and Intl timezone formatting is unavailable. The application
// calculations below must continue to use their bundled database.
const originalDateTimeFormat = Intl.DateTimeFormat;
const originalGetTimezoneOffset = Date.prototype.getTimezoneOffset;
Intl.DateTimeFormat = function spoofedDateTimeFormat() {
  throw new Error("timezone formatting was intercepted");
};
Date.prototype.getTimezoneOffset = () => 0;

// These zones do not observe DST, so their expected current offsets are
// deterministic. Together they catch the regression where every option was
// labelled UTC+0 as well as whole-hour and fractional-hour formatting bugs.
const cases = [
  ["UTC", "UTC+0"],
  ["Asia/Riyadh", "UTC+3"],
  ["Asia/Rangoon", "UTC+6:30"],
  ["Asia/Shanghai", "UTC+8"],
  ["Asia/Seoul", "UTC+9"],
  ["Asia/Kathmandu", "UTC+5:45"],
];

const instant = Date.UTC(2026, 7, 16, 1, 2, 3);
for (const [zone, expected] of cases) {
  assert.equal(timeZoneOffsetLabel(zone, instant), expected, zone);
}
assert.equal(timeZoneOffsetLabel("Mars/Olympus_Mons"), "");
assert.ok(timeZoneNames().includes("Asia/Shanghai"));
assert.deepEqual(zonedDateParts(new Date(instant), "Asia/Shanghai"), {
  year: 2026,
  month: 8,
  day: 16,
  hour: 9,
  minute: 2,
  second: 3,
});

// The database, rather than the host environment, must apply DST rules.
assert.equal(timeZoneOffsetLabel("America/New_York", Date.UTC(2026, 0, 15)), "UTC-5");
assert.equal(timeZoneOffsetLabel("America/New_York", Date.UTC(2026, 6, 15)), "UTC-4");

Intl.DateTimeFormat = originalDateTimeFormat;
Date.prototype.getTimezoneOffset = originalGetTimezoneOffset;
