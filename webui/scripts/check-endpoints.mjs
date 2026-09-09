#!/usr/bin/env node
// Fails if the built bundle contains a hardcoded API endpoint (QUM-1349).
//
// The SPA must talk to the API same-origin over a relative /api path, so that
// one image runs against any deployment. A baked-in host would work in dev and
// silently point at the wrong place — or at nothing — everywhere else.
//
// Scope, stated because it bounds what a pass means: this greps for absolute
// http(s) URLs in dist/ and allows two classes that are NOT endpoints —
// XML/SVG namespace URIs, and the URL-parsing bases and doc links that React
// and react-router carry in their own source. Anything else is a failure.
// A dist/ that does not exist, or contains no JS, is also a failure: an empty
// scan must never read as a clean one.
import { readdirSync, readFileSync, statSync } from "node:fs";
import { join } from "node:path";

const DIST = new URL("../dist/", import.meta.url).pathname;

const ALLOW = [
  /^https?:\/\/(www\.)?w3\.org\//,
  /^http:\/\/localhost$/, // react-router / React internal URL-parsing base
  /^https:\/\/react\.dev\//,
  /^https:\/\/reactrouter\.com\//,
];

function walk(dir) {
  return readdirSync(dir).flatMap((name) => {
    const p = join(dir, name);
    return statSync(p).isDirectory() ? walk(p) : [p];
  });
}

let files;
try {
  files = walk(DIST);
} catch {
  console.error(`FAIL: no build output at ${DIST} — run \`npm run build\` first.`);
  process.exit(1);
}

if (!files.some((f) => f.endsWith(".js"))) {
  console.error(`FAIL: ${DIST} contains no JS bundle; the scan would be vacuous.`);
  process.exit(1);
}

const offenders = new Map();
for (const file of files) {
  const text = readFileSync(file, "utf8");
  for (const url of text.match(/https?:\/\/[^"'`\s)]*/g) ?? []) {
    const bare = url.replace(/[.,;]+$/, "");
    if (ALLOW.some((re) => re.test(bare))) continue;
    offenders.set(bare, file);
  }
}

if (offenders.size > 0) {
  console.error("FAIL: absolute URL(s) in the built bundle:");
  for (const [url, file] of offenders) console.error(`  ${url}  (${file})`);
  process.exit(1);
}

console.log(`OK: ${files.length} built file(s) scanned, no hardcoded endpoint.`);
