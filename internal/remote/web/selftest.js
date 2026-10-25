// Browser runner of tests.js for selftest.html.

import { run } from './tests.js';

const ul = document.getElementById('results');
run((ok, name) => {
  const li = document.createElement('li');
  li.className = 'result ' + (ok ? 'pass' : 'fail');
  li.textContent = (ok ? 'ok   ' : 'FAIL ') + name;
  ul.append(li);
}).then(({ pass, fail }) => {
  const h = document.getElementById('summary');
  h.textContent = `${pass} passed, ${fail} failed`;
  h.className = fail ? 'fail' : 'pass';
}, e => {
  document.getElementById('summary').textContent = 'error: ' + e;
});
