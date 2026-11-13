// i18n.test.mjs — tests de i18n.js avec un DOM minimal simulé
import assert from 'node:assert/strict';
import { mkdtempSync, writeFileSync, readFileSync } from 'node:fs';
import { join } from 'node:path';
import { pathToFileURL } from 'node:url';

const base = process.env.TMPDIR || (process.env.PREFIX ? process.env.PREFIX + '/tmp' : '/tmp');
const dir = mkdtempSync(join(base, 'i18n-'));
writeFileSync(join(dir, 'ui-config.mjs'), readFileSync('./ui-config.js', 'utf8'));
writeFileSync(join(dir, 'i18n.mjs'), readFileSync('./i18n.js', 'utf8').replace("'./ui-config.js'", "'./ui-config.mjs'"));

const mk = (attrs) => ({ attrs, textContent: '', title: '', set: {}, getAttribute(k) { return this.attrs[k] ?? null; }, setAttribute(k, v) { this.set[k] = v; } });
const nText = mk({ 'data-i18n': 'balance.label' });
const nAria = mk({ 'data-i18n-aria': 'aria.menu' });
const store = {};
globalThis.localStorage = { getItem: (k) => store[k] ?? null, setItem: (k, v) => { store[k] = v; } };
globalThis.location = { search: '?lang=en&token=x' };
globalThis.document = {
  documentElement: { lang: '' },
  querySelectorAll: (sel) => (sel === '[data-i18n]' ? [nText] : sel === '[data-i18n-aria]' ? [nAria] : []),
};

const m = await import(pathToFileURL(join(dir, 'i18n.mjs')).href);
let n = 0;
const ok = (x) => console.log('OK', ++n, x);

assert.equal(m.initLang(), 'en');
assert.equal(store['223crash.lang.v1'], undefined);
assert.equal(nText.textContent, 'BALANCE');
assert.equal(document.documentElement.lang, 'en');
ok('?lang=en prioritaire, non persisté, DOM mis à jour');

assert.ok(m.t('phase.betting', { s: '3.2' }).includes('3.2'));
assert.equal(m.t('clé.inconnue'), 'clé.inconnue');
ok('variables remplacées, clé inconnue renvoyée brute');

const enFlying = m.t('phase.flying');
let fired = 0;
const off = m.onLangChange(() => { fired++; });
m.registerLanguage('bm', 'Test', { 'balance.label': 'TEST-BAL' });
assert.equal(m.setLang('bm'), true);
assert.equal(m.t('balance.label'), 'TEST-BAL');
assert.equal(m.t('phase.flying'), enFlying);
assert.equal(nText.textContent, 'TEST-BAL');
assert.equal(fired, 1);
assert.equal(store['223crash.lang.v1'], 'bm');
ok('langue ajoutée à chaud, repli sur en pour les clés manquantes, persistance');

assert.equal(m.setLang('zz'), false);
off();
assert.ok(m.languages().some((l) => l.code === 'fr') && m.languages().some((l) => l.code === 'bm'));
ok('langue inconnue refusée, liste des langues');

console.log('TOUS LES TESTS OK (' + n + ')');
