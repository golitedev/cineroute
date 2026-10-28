// Run with: node scripts/check-i18n.mjs (no emitted files or network calls).
import assert from 'node:assert/strict';
import { createRequire } from 'node:module';
import fs from 'node:fs';
const require = createRequire(import.meta.url);
const ts = require('typescript');
require.extensions['.ts'] = (module, filename) => {
    const source = ts.transpileModule(fs.readFileSync(filename, 'utf8'), {
        compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2020, esModuleInterop: true }
    }).outputText;
    module._compile(source, filename);
};
const { en } = require('../lib/i18n/en.ts');
const { es } = require('../lib/i18n/es.ts');
const { sv } = require('../lib/i18n/sv.ts');
const { translator } = require('../lib/i18n/core.ts');
const { ApiError, formatApiError, formatAccountError, formatJobMessage, formatProblem } = require('../lib/cineroute-api.ts');
const placeholders = (value) => [...value.matchAll(/\{(\w+)\}/g)].map((match) => match[1]).sort();
const codes =
    'queued starting downloading fixing_flac importing cancelled cancelling already_in_library interrupted_restart interrupted_shutdown retry_wait checking added incomplete has_problems no_tracks timeout failure_auth failure_ineligible failure_app_id failure_rate_limited failure_disk_full failure_missing_credentials failure_crash error_album error_staging error_streamrip error_verify error_fix error_naming error_move_aside error_import'.split(
        ' '
    );
const reasons =
    'missing too_short too_long preview_sample decode_failed empty_file unreadable unexpected_file duplicate partial_file file_error streamrip more'.split(' ');
const args = { attempt: 2, seconds: 3, tracks: 1, format: 'FLAC', bits: 24, khz: 96, failed: 2, total: 10, minutes: 20, error: 'RAW ERROR' };
for (const [language, dictionary] of Object.entries({ en, es, sv })) {
    assert.deepEqual(Object.keys(dictionary).sort(), Object.keys(en).sort());
    for (const key of Object.keys(en)) {
        assert.ok(dictionary[key].trim(), `${language}: empty ${key}`);
        assert.deepEqual(placeholders(dictionary[key]), placeholders(en[key]), `${language}: placeholders in ${key}`);
    }
    const t = translator(language);
    assert.equal(t('trackCount', { count: 1 }), t('trackCount.one', { count: 1 }));
    assert.equal(t('trackCount', { count: 2 }), t('trackCount.other', { count: 2 }));
    assert.equal(t('trackCount', { count: 1000000 }), t('trackCount.other', { count: 1000000 }));
    for (const code of codes) {
        const text = formatJobMessage({ message: 'FALLBACK', message_code: code, message_args: args }, t);
        assert.ok(text && text !== 'FALLBACK' && !/\{\w+\}/.test(text), `${language}: ${code}`);
        if (code.startsWith('error_')) assert.ok(text.includes('RAW ERROR'));
    }
    for (const code of ['', 'unknown_future']) {
        assert.equal(formatJobMessage({ message: 'FALLBACK', message_code: code }, t), 'FALLBACK');
        assert.equal(formatApiError(new ApiError('FALLBACK', 500, code), t), 'FALLBACK');
        assert.equal(formatAccountError({ qobuz_error: 'FALLBACK', qobuz_error_code: code }, t), 'FALLBACK');
    }
    for (const key of Object.keys(en).filter((key) => key.startsWith('api.'))) {
        const text = formatApiError(new ApiError('RAW ERROR', 400, key.slice(4)), t);
        assert.equal(text, t(key, { error: 'RAW ERROR' }));
    }
    for (const reason of reasons) {
        const item = { disc: 2, track: 7, title: 'Song', reason, detail: reason === 'more' ? '5' : 'RAW DETAIL', unavailable: true, text: 'FALLBACK' };
        const text = formatProblem(item, t);
        assert.ok(text && text !== 'FALLBACK' && !/\{\w+\}/.test(text), `${language}: ${reason}`);
        if (!['streamrip', 'more'].includes(reason)) assert.ok(text.includes(t('problem.disc', { disc: 2 })));
    }
    assert.equal(formatProblem({ reason: 'future', text: 'FALLBACK' }, t), 'FALLBACK');
    const suffixes = formatJobMessage({ message_code: 'added', message_args: { ...args, rate_limited: true, tries: 2, kept_path: '/raw/path' } }, t);
    assert.ok(suffixes.includes(t('job.failure_rate_limited')) && suffixes.includes(t('job.tries', { count: 2 })) && suffixes.includes('/raw/path'));
    const noDisc = formatProblem({ disc: 1, track: 0, title: 'Song', reason: 'missing', detail: '', unavailable: false }, t);
    assert.equal(noDisc, `"Song": ${t('problem.missing')}`);
}
// Runtime fallback protects against an older or incomplete dictionary bundle.
const original = es.tagline;
delete es.tagline;
assert.equal(translator('es')('tagline'), en.tagline);
es.tagline = original;
console.log('i18n dictionaries, plural/interpolation, all API/job/problem codes, suffixes and English fallbacks passed.');
