// Runs the PWA self tests (tests.js) with node, without a browser:
//   node internal/remote/web/selftest.mjs
// The same checks run in a browser from /selftest.html.

import { run } from './tests.js';

const { pass, fail } = await run((ok, name) => console.log((ok ? 'ok   ' : 'FAIL ') + name));
console.log(`${pass} passed, ${fail} failed`);
process.exitCode = fail ? 1 : 0;
